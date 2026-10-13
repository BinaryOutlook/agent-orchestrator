package reconcile

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

// cleanupLookupStore answers ProjectCleanupCommands with fixed commands or an
// error.
type cleanupLookupStore struct {
	lifecycleStore
	commands []string
	err      error
}

func (s *cleanupLookupStore) ProjectCleanupCommands(context.Context, string, string) ([]string, error) {
	return s.commands, s.err
}

// Delete receives the project's current Cleanup commands, including an empty
// list for a project that no longer has Cleanup; when they can't be read, or
// the store can't read them, Delete gets no list and the provider falls back
// to the commands the worker installed.
func TestDeletionCarriesTheProjectsCurrentCleanup(t *testing.T) {
	record := domain.Sandbox{OrgID: "org-1", SessionID: "session-1"}
	reconciler := func(store Store) *Reconciler {
		return New(store, lifecycleResolver{}, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}

	ctx := reconciler(&cleanupLookupStore{commands: []string{"docker compose down"}}).withCleanupCommands(context.Background(), record)
	if commands, ok := sandbox.CleanupCommands(ctx); !ok || strings.Join(commands, "|") != "docker compose down" {
		t.Fatalf("current Cleanup = %v (%v), want the project's commands", commands, ok)
	}

	ctx = reconciler(&cleanupLookupStore{}).withCleanupCommands(context.Background(), record)
	if commands, ok := sandbox.CleanupCommands(ctx); !ok || len(commands) != 0 {
		t.Fatalf("a project without Cleanup = %v (%v), want a known empty list", commands, ok)
	}

	ctx = reconciler(&cleanupLookupStore{err: errors.New("project deleted")}).withCleanupCommands(context.Background(), record)
	if _, ok := sandbox.CleanupCommands(ctx); ok {
		t.Fatal("a failed lookup must leave the provider's installed commands in charge")
	}

	ctx = reconciler(&lifecycleStore{}).withCleanupCommands(context.Background(), record)
	if _, ok := sandbox.CleanupCommands(ctx); ok {
		t.Fatal("a store without the lookup must leave the provider's installed commands in charge")
	}
}
