package mobilebridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const lidHelperVersion = 1
const lidHelperLabel = "com.aoagents.mobile-wake"

// LidAwake is a lease client for the independent, launchd-owned coordinator.
// All AO data directories for this login use one coordinator under ~/.ao/wake;
// per-data-directory coordinators would race over a global kernel control.
type LidAwake struct {
	mu                    sync.Mutex
	binary, directory, id string
	pid                   int
	started, sequence     uint64
	supported             bool
	reason                string
	cancel                context.CancelFunc
	done                  chan struct{}
}

type lidLease struct {
	ID        string  `json:"id"`
	PID       int     `json:"pid"`
	Started   uint64  `json:"started"`
	Sequence  uint64  `json:"sequence"`
	WrittenAt float64 `json:"writtenAt"`
}

type lidStatus struct {
	Version   int               `json:"version"`
	UpdatedAt float64           `json:"updatedAt"`
	Active    bool              `json:"active"`
	Reason    string            `json:"reason"`
	Leases    []string          `json:"leases"`
	Blocked   map[string]string `json:"blocked"`
}

// NewLidAwake probes without changing power state or registering a job. The
// helper is optional for CLI-only/non-desktop installations.
func NewLidAwake(pid int, binary string) *LidAwake {
	l := &LidAwake{pid: pid, binary: binary, reason: "helper_missing"}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		l.reason = "unsupported"
		return l
	}
	if binary == "" || !filepath.IsAbs(binary) {
		return l
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return l
	}
	l.directory = filepath.Join(home, ".ao", "wake")
	ctx, cancel := context.WithTimeout(context.Background(), processProbeTimeout)
	defer cancel()
	out, err := aoprocess.CommandContext(ctx, binary, "probe").Output()
	if err != nil {
		l.reason = "native_failed"
		return l
	}
	var probe struct {
		Supported bool   `json:"supported"`
		Started   uint64 `json:"started"`
		Version   int    `json:"version"`
	}
	if json.Unmarshal(out, &probe) != nil || probe.Version != lidHelperVersion || probe.Started == 0 {
		l.reason = "native_failed"
		return l
	}
	l.supported, l.started = probe.Supported, probe.Started
	if !l.supported {
		l.reason = "unsupported"
		return l
	}
	l.reason = ""
	return l
}

// Status separates a saved request from the coordinator's fresh acknowledgement.
func (l *LidAwake) Status() (supported, active bool, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.supported {
		return false, false, l.reason
	}
	if l.id == "" {
		return true, false, l.reason
	}
	st, err := l.readStatus()
	if err != nil {
		return true, false, "helper_unavailable"
	}
	if blocked := st.Blocked[l.id]; blocked != "" {
		return true, false, blocked
	}
	for _, id := range st.Leases {
		if id == l.id {
			return true, st.Active, st.Reason
		}
	}
	if st.Reason != "" {
		return true, false, st.Reason
	}
	if l.cancel == nil {
		l.id, l.reason = "", ""
		return true, false, ""
	}
	return true, false, "starting"
}

func (l *LidAwake) readStatus() (lidStatus, error) {
	var st lidStatus
	file, err := os.Open(filepath.Join(l.directory, "status.json"))
	if err != nil {
		return st, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return st, err
	}
	if len(data) > 65536 {
		return st, errors.New("oversized wake status")
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	age := float64(time.Now().UnixMilli())/1000 - st.UpdatedAt
	if st.Version != lidHelperVersion || age < -5 || age > 6 {
		return st, errors.New("stale wake status")
	}
	return st, nil
}

// Start requires explicit user opt-in. Registration never runs on boot merely
// because the helper binary exists. A restored preference can renew an already
// registered helper, but cannot silently install one.
func (l *LidAwake) Start(allowSetup bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.supported {
		return errors.New("closed-lid wakefulness is unavailable on this host")
	}
	if l.cancel != nil {
		return nil
	}
	if !allowSetup {
		if _, err := os.Stat(filepath.Join(l.directory, "agent.plist")); err != nil {
			l.reason = "setup_needed"
			return nil
		}
	}
	if err := l.setup(); err != nil {
		l.reason = "setup_failed"
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	random[6] = (random[6] & 0x0f) | 0x40
	random[8] = (random[8] & 0x3f) | 0x80
	h := hex.EncodeToString(random[:])
	l.id = h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	l.sequence = 0
	if err := l.renew(); err != nil {
		l.reason = "lease_failed"
		_ = os.Remove(filepath.Join(l.directory, "leases", l.id+".json"))
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	l.cancel, l.done, l.reason = cancel, done, ""
	go l.run(ctx, done)
	return nil
}

func (l *LidAwake) renew() error {
	l.sequence++
	lease := lidLease{l.id, l.pid, l.started, l.sequence, float64(time.Now().UnixMilli()) / 1000}
	data, err := json.Marshal(lease)
	if err != nil {
		return err
	}
	return writeWakeFile(filepath.Join(l.directory, "leases", l.id+".json"), data, 0o600)
}

func (l *LidAwake) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			// Stop does not clear cancel until this goroutine has returned.
			st, err := l.readStatus()
			if err == nil && st.Blocked[l.id] != "" {
				// Keep the same lease identity while latched. Only an explicit off/on
				// generates a new identity and permits the native coordinator to rearm.
				l.reason = st.Blocked[l.id]
			}
			if err = l.renew(); err != nil {
				l.reason = "lease_failed"
			}
			l.mu.Unlock()
		}
	}
}

// Stop withdraws this daemon's lease and waits for native cleanup acknowledgement.
func (l *LidAwake) Stop() {
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	l.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.id != "" {
		if err := os.Remove(filepath.Join(l.directory, "leases", l.id+".json")); err != nil && !os.IsNotExist(err) {
			l.cancel, l.done, l.reason = nil, nil, "lease_failed"
			return
		}
		// Wait for acknowledgement of this lease's release, not another
		// daemon's global activation. Do not claim cleanup on a missing helper.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			st, err := l.readStatus()
			if err == nil {
				owned := false
				for _, id := range st.Leases {
					if id == l.id {
						owned = true
					}
				}
				if !owned && st.Reason != "recovery_failed" {
					l.cancel, l.done, l.id, l.reason = nil, nil, "", ""
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		l.cancel, l.done, l.reason = nil, nil, "recovery_failed"
		return
	}
	l.cancel, l.done, l.reason = nil, nil, ""

}

func secureWakeDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm()&0o077 != 0 {
		return errors.New("wake directory must be private and not a symlink")
	}
	return nil
}

func writeWakeFile(name string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".wake-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporary, name); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return parent.Sync()
}

func (l *LidAwake) setup() error {
	for _, directory := range []string{filepath.Dir(l.directory), l.directory, filepath.Join(l.directory, "leases")} {
		if err := secureWakeDirectory(directory); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(l.binary)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	// Immutable version paths let an old registered job recover safely across an
	// app replacement; no launchd job points into a disappearing .app bundle.
	binary := filepath.Join(l.directory, "ao-wake-"+hex.EncodeToString(digest[:12]))
	if err := writeWakeFile(binary, data, 0o700); err != nil {
		return err
	}
	escape := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	plist := []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>%s</string><key>ProgramArguments</key><array><string>%s</string><string>serve</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>2</integer><key>ProcessType</key><string>Background</string></dict></plist>
`, lidHelperLabel, escape(binary)))
	name := filepath.Join(l.directory, "agent.plist")
	previous, readErr := os.ReadFile(name)
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if readErr == nil && !bytes.Equal(previous, plist) {
		// Version replacement first releases old ownership (or leaves its journal
		// for the new coordinator), then installs the next fixed executable.
		_ = aoprocess.CommandContext(ctx, "/bin/launchctl", "bootout", domain+"/"+lidHelperLabel).Run()
	}
	if err := writeWakeFile(name, plist, 0o600); err != nil {
		return err
	}
	if err := aoprocess.CommandContext(ctx, "/bin/launchctl", "bootstrap", domain, name).Run(); err != nil {
		// bootstrap also fails when the exact job is already registered.
		if check := aoprocess.CommandContext(ctx, "/bin/launchctl", "print", domain+"/"+lidHelperLabel).Run(); check != nil {
			return err
		}
	}
	return nil
}

// Setup registers the fixed recovery executable without acquiring wake ownership.
func (l *LidAwake) Setup() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.setup(); err != nil {
		l.reason = "setup_failed"
		return err
	}
	l.reason = ""
	return nil
}
