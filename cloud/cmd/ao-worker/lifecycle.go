package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/activity"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// setupMessageOutputLimit bounds the command output quoted in the startup
// error a failed Setup shows on the session.
const setupMessageOutputLimit = 2048

// readWorkingDirectory reads a bring-your-own Coder connection's working
// directory and removes it from the process environment, so the coding agent
// and its tools never inherit it.
func readWorkingDirectory() string {
	defer func() { _ = os.Unsetenv(worker.WorkingDirectoryEnv) }()
	return strings.TrimSpace(os.Getenv(worker.WorkingDirectoryEnv))
}

// resolveConfiguredWorkspace settles the session on the connection's working
// directory. An adopted checkout gets the same process-scoped git setup as a
// reused one; a directory AO clones into is treated like its own checkout.
func resolveConfiguredWorkspace(
	ctx context.Context,
	logger *slog.Logger,
	bootstrap worker.BootstrapResponse,
	template, defaultWorkspace, dataDir string,
) (worker.ReuseDecision, error) {
	home := strings.TrimSpace(os.Getenv("HOME"))
	decision, err := worker.ResolveWorkingDirectory(
		ctx, worker.ExecGitRunner{}, template, home, dataDir,
		bootstrap.Launch.RepositoryURL, bootstrap.SessionID,
		bootstrap.Launch.Branch, bootstrap.Launch.DefaultBranch,
	)
	if err != nil {
		logger.Error("working directory: the configured directory cannot be used",
			"working_directory", template, "error", err)
		return worker.ReuseDecision{Workspace: defaultWorkspace}, err
	}
	if decision.Reused {
		logger.Info("working directory: working in the template's existing checkout",
			"workspace", decision.Workspace, "adopted_now", decision.AdoptedNow, "stash", decision.Stash)
		for key, value := range worker.ReusedCheckoutGitEnvironment(dataDir) {
			_ = os.Setenv(key, value)
		}
	} else {
		logger.Info("working directory: cloning the repository into the configured directory",
			"workspace", decision.Workspace)
	}
	_ = os.Setenv("AO_WORKSPACE_DIR", decision.Workspace)
	return decision, nil
}

// runProjectSetup runs the project's Setup commands (Project settings >
// Scripts) once per session, after the checkout and before the coding agent
// starts, and reports the outcome on the session's event stream. Like a
// local project, a failed step stops the session from starting its agent: it
// returns the startup error to show, and the next session open retries.
func runProjectSetup(
	ctx context.Context,
	logger *slog.Logger,
	client *client,
	dataDir string,
	commands []string,
	workspace string,
	environment worker.SessionScriptEnvironment,
) (failure string) {
	if len(commands) == 0 {
		return ""
	}
	logger.Info("project setup: running", "workspace", workspace, "steps", len(commands))
	event, ran := worker.SetupRunner{DataDir: dataDir}.Run(ctx, commands, workspace, environment)
	if !ran {
		logger.Info("project setup: already ran for this session")
		return ""
	}
	attrs := []any{
		"status", event.Status, "step", event.Step, "exit_code", event.ExitCode,
		"duration_ms", event.DurationMS, "output", activity.Redact(event.Output),
	}
	event.Output = activity.Excerpt(event.Output, setupMessageOutputLimit)
	if event.Status == worker.SetupSucceeded {
		logger.Info("project setup: finished", attrs...)
	} else {
		logger.Warn("project setup: a step did not succeed; the agent will not start", attrs...)
		failure = event.Message()
	}
	if err := client.publishEvent(ctx, worker.SetupEventType, event); err != nil {
		logger.Warn("publish project setup outcome", "error", err)
	}
	return failure
}
