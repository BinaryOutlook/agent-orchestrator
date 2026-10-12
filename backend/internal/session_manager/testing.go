package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/skillassets"
)

// ErrTestingHarness means a Litmus testing session was asked for on anything
// but a claude-code TUI worker: its tool policy is enforced only there.
var ErrTestingHarness = errors.New("session: testing sessions run on the claude-code harness as TUI workers")

type testingSpawnKey struct{}

// WithTestingProfile marks the spawn made with ctx as a Litmus testing session:
// it works in its run dir with no worktree or branch, and launches under the
// fixed tool policy. Restores recognise such a session by its workspace path.
func WithTestingProfile(ctx context.Context) context.Context {
	return context.WithValue(ctx, testingSpawnKey{}, true)
}

func isTestingSpawn(ctx context.Context) bool {
	on, _ := ctx.Value(testingSpawnKey{}).(bool)
	return on
}

// createTestingWorkspace makes the session's run dir its working directory.
func createTestingWorkspace(dataDir string, id domain.SessionID, projectID domain.ProjectID) (ports.WorkspaceInfo, error) {
	dir := litmus.RunDir(dataDir, string(id))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return ports.WorkspaceInfo{}, err
	}
	return ports.WorkspaceInfo{Path: dir, SessionID: id, ProjectID: projectID}, nil
}

// applyTestingLaunch gives a launch in a run dir the unattended permission mode and the tool lists.
func applyTestingLaunch(cfg *ports.LaunchConfig, dataDir string) {
	if litmus.IsRunDir(dataDir, cfg.WorkspacePath) {
		cfg.Permissions = ports.PermissionModeDontAsk
		cfg.AllowedTools, cfg.DisallowedTools = litmus.ToolPolicy()
	}
}

// applyTestingRestore is applyTestingLaunch for the native resume command.
func applyTestingRestore(cfg *ports.RestoreConfig, dataDir string) {
	if litmus.IsRunDir(dataDir, cfg.Session.WorkspacePath) {
		cfg.Permissions = ports.PermissionModeDontAsk
		cfg.AllowedTools, cfg.DisallowedTools = litmus.ToolPolicy()
	}
}

// applyTestingEnv names the run in the environment of a session working in its run dir.
func applyTestingEnv(env map[string]string, dataDir string, id domain.SessionID, workspacePath string) {
	if litmus.IsRunDir(dataDir, workspacePath) {
		env[litmus.EnvRun] = litmus.RunID(string(id))
	}
}

// isTestingSession reports whether id is a Litmus testing session: the spawn that
// is making it (marked on ctx), or a session whose stored workspace is a run dir.
// The run dir alone is not proof: any session's `ao testing intake` creates one.
func (m *Manager) isTestingSession(ctx context.Context, id domain.SessionID) bool {
	if isTestingSpawn(ctx) {
		return true
	}
	rec, found, err := m.store.GetSession(ctx, id)
	return err == nil && found && litmus.IsRunDir(m.dataDir, rec.Metadata.WorkspacePath)
}

// ErrTestingSessionFixed means a testing session was asked to change its harness
// or interface: the tool policy is applied only at spawn and relaunch.
var ErrTestingSessionFixed = errors.New("session: a testing session cannot change its harness or interface")

func (m *Manager) refuseTestingSession(ctx context.Context, id domain.SessionID) error {
	if m.isTestingSession(ctx, id) {
		return fmt.Errorf("%w: %s", ErrTestingSessionFixed, id)
	}
	return nil
}

// litmusSkillPointer replaces the generic ao guide in a testing session's system
// prompt: that guide steers toward previews and builds, which the policy denies.
func (m *Manager) litmusSkillPointer() string {
	skillFile := filepath.ToSlash(filepath.Join(skillassets.LitmusDir(m.dataDir), "SKILL.md"))
	return "\n\n## Testing a pull request\n\n" +
		"You are a Litmus tester. Read `" + skillFile + "` and follow it. Do not load other ao guides."
}
