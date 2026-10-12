//go:build !windows

package mobilebridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLidStatusRequiresFreshOwnLease(t *testing.T) {
	for _, tc := range []struct {
		name       string
		age        float64
		ids        []string
		blocked    string
		wantActive bool
		reason     string
	}{
		{"own lease", 0, []string{"mine"}, "", true, ""},
		{"other daemon", 0, []string{"other"}, "", false, "starting"},
		{"stale", 10, []string{"mine"}, "", false, "helper_unavailable"},
		{"future", -10, []string{"mine"}, "", false, "helper_unavailable"},
		{"safety latch", 0, []string{"mine"}, "low_battery", false, "low_battery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			st := lidStatus{Version: lidHelperVersion, UpdatedAt: float64(time.Now().UnixMilli())/1000 - tc.age, Active: true, Leases: tc.ids, Blocked: map[string]string{"mine": tc.blocked}}
			data, _ := json.Marshal(st)
			if err := writeWakeFile(filepath.Join(directory, "status.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			l := &LidAwake{supported: true, directory: directory, id: "mine", cancel: func() {}}
			supported, active, reason := l.Status()
			if !supported || active != tc.wantActive || reason != tc.reason {
				t.Fatalf("got (%v,%v,%q)", supported, active, reason)
			}
		})
	}
}

func TestLidLeaseRenewalAndStop(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "leases"), 0700); err != nil {
		t.Fatal(err)
	}
	l := &LidAwake{directory: directory, id: "mine", pid: 42, started: 99}
	for i := uint64(1); i <= 2; i++ {
		if err := l.renew(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(directory, "leases/mine.json"))
		if err != nil {
			t.Fatal(err)
		}
		var lease lidLease
		if err := json.Unmarshal(data, &lease); err != nil {
			t.Fatal(err)
		}
		if lease.Sequence != i || lease.PID != 42 || lease.Started != 99 {
			t.Fatalf("wrong lease: %+v", lease)
		}
	}
	ack, _ := json.Marshal(lidStatus{Version: lidHelperVersion, UpdatedAt: float64(time.Now().UnixMilli()) / 1000})
	if err := writeWakeFile(filepath.Join(directory, "status.json"), ack, 0600); err != nil {
		t.Fatal(err)
	}
	l.Stop()
	if _, err := os.Stat(filepath.Join(directory, "leases/mine.json")); !os.IsNotExist(err) {
		t.Fatalf("lease survives stop: %v", err)
	}
	l.Stop()
}

func TestWakeDirectoryRejectsSymlinkAndPublicPermissions(t *testing.T) {
	directory := t.TempDir()
	link := filepath.Join(directory, "link")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	if err := secureWakeDirectory(link); err == nil {
		t.Fatal("accepted symlink")
	}
	public := filepath.Join(directory, "public")
	if err := os.Mkdir(public, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := secureWakeDirectory(public); err == nil {
		t.Fatal("accepted public directory")
	}
}
