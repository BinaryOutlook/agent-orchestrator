package telemetry

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// OptOutFile is the marker the desktop app writes when the user turns off
// telemetry in Settings. It lives in the data dir so the daemon, the desktop
// app and (via the identity route) the phone all read one switch.
const OptOutFile = "telemetry_opt_out"

const (
	installIDFile = "telemetry_install_id"
	// boundUserFile remembers which AO Cloud user the install ID may be linked
	// to. The desktop identify() merges the install ID into that person for good,
	// so once the user changes or signs out the ID is retired.
	boundUserFile = "telemetry_bound_user"
)

var (
	cloudUserIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// Identity is the daemon's live telemetry identity: the install ID plus the
// two optional person-level attributes. Safe for concurrent use.
type Identity struct {
	dataDir    string
	optOutPath string
	fresh      bool

	mu          sync.RWMutex
	installID   string
	cloudUserID string
	githubLogin string
}

// NewIdentity loads (or creates) the install ID under dataDir.
func NewIdentity(dataDir string) (*Identity, error) {
	id, err := loadOrCreateInstallID(dataDir)
	if err != nil {
		return nil, err
	}
	return &Identity{dataDir: dataDir, installID: id, optOutPath: filepath.Join(dataDir, OptOutFile), fresh: freshInstall(dataDir)}, nil
}

// FreshInstall reports whether this process is the first run of a brand new
// install, the one moment ao.app.installed is emitted.
func (i *Identity) FreshInstall() bool { return i.fresh }

// freshInstall is true when no event has ever been exported (the tenure file is
// written on the first one) and the install ID was created in the last day. The
// age check keeps an upgrade from a build that predates tenure from counting
// as a new install.
func freshInstall(dataDir string) bool {
	if _, err := os.Stat(filepath.Join(dataDir, tenureStateFile)); err == nil {
		return false
	}
	info, err := os.Stat(filepath.Join(dataDir, installIDFile))
	return err == nil && time.Since(info.ModTime()) < 24*time.Hour
}

// OptedOut reports whether the user switched telemetry off.
// ponytail: one stat per exported event, cached nowhere; events are rate
// limited upstream, add an mtime cache if that ever stops being true.
func (i *Identity) OptedOut() bool {
	_, err := os.Stat(i.optOutPath)
	return err == nil
}

// InstallID is the current anonymous install ID. It changes when an account
// sign-out or switch retires the previous one.
func (i *Identity) InstallID() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.installID
}

// SetCloudUserID implements ports.TelemetryIdentityStore. The install ID is
// aliased into the first account that signs in on the desktop and PostHog never
// splits it again, so a sign-out or a different account retires it for a fresh
// anonymous one. Without that, signed-out or next-account daemon events would
// keep resolving to the first account.
func (i *Identity) SetCloudUserID(id string) bool {
	id = strings.TrimSpace(id)
	if id != "" && !cloudUserIDPattern.MatchString(id) {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.cloudUserID = id
	bound := i.readBoundUser()
	// ponytail: persistence is best effort; a failed write only means the old
	// install ID is kept, which is what happened before rotation existed.
	switch {
	case id == "" && bound != "", id != "" && bound != "" && bound != id:
		if next, err := replaceInstallID(i.dataDir); err == nil {
			i.installID = next
		}
		if id == "" {
			_ = os.Remove(filepath.Join(i.dataDir, boundUserFile))
		} else {
			_ = os.WriteFile(filepath.Join(i.dataDir, boundUserFile), []byte(id+"\n"), 0o600)
		}
	case id != "" && bound == "":
		_ = os.WriteFile(filepath.Join(i.dataDir, boundUserFile), []byte(id+"\n"), 0o600)
	}
	return true
}

func (i *Identity) readBoundUser() string {
	b, err := os.ReadFile(filepath.Join(i.dataDir, boundUserFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SetGitHubLogin implements ports.TelemetryIdentityStore. A login that does not
// look like one is dropped rather than exported.
func (i *Identity) SetGitHubLogin(login string) {
	login = strings.TrimSpace(login)
	if login != "" && !githubLoginPattern.MatchString(login) {
		return
	}
	i.mu.Lock()
	i.githubLogin = login
	i.mu.Unlock()
}

// Snapshot implements ports.TelemetryIdentityStore. Opted-out installs report
// no user attributes at all.
func (i *Identity) Snapshot() ports.TelemetryIdentity {
	snap := ports.TelemetryIdentity{OptedOut: i.OptedOut()}
	i.mu.RLock()
	defer i.mu.RUnlock()
	snap.InstallID = i.installID
	if !snap.OptedOut {
		snap.CloudUserID, snap.GitHubLogin = i.cloudUserID, i.githubLogin
	}
	return snap
}
