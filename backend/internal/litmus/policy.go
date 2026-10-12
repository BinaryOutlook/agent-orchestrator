package litmus

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// EnvRun is set in a testing session's environment to the id of the run it owns.
const EnvRun = "AO_LITMUS_RUN"

// SweepInterval is how often the daemon looks for runs past their deadline.
const SweepInterval = 15 * time.Second

// A testing worker may drive the browser verbs and read files; it may not edit,
// browse the web, or run builds. The wrapper shells are denied by name because
// a deny rule on a program does not match the same program inside `sh -c`.
var (
	allowedTools    = []string{"Bash(ao testing:*)", "Bash(ao report:*)", "Read", "Grep", "Glob", "LS"}
	disallowedTools = []string{"Edit", "Write", "MultiEdit", "NotebookEdit", "WebFetch", "WebSearch",
		"Bash(npm:*)", "Bash(npx:*)", "Bash(node:*)", "Bash(go:*)", "Bash(make:*)", "Bash(sh:*)", "Bash(bash:*)",
		"Bash(zsh:*)", "Bash(python:*)", "Bash(python3:*)", "Bash(git:*)", "Bash(curl:*)", "Bash(rm:*)",
		// A tester must not spawn more testers or touch other sessions. Deny beats allow.
		"Bash(ao testing start:*)", "Bash(ao spawn:*)", "Bash(ao session:*)", "Bash(ao send:*)"}
)

// ToolPolicy returns copies of the allow and deny lists for a testing session.
func ToolPolicy() (allowed, disallowed []string) {
	return slices.Clone(allowedTools), slices.Clone(disallowedTools)
}

// RunID is the id of the one run a session owns.
func RunID(sessionID string) string {
	id, _ := runID(sessionID)
	return id
}

// RunDir is a testing session's working directory: the directory its run keeps
// context-pack.md, checks.json, receipts/ and verdict.md in.
func RunDir(dataDir, sessionID string) string {
	return filepath.Join(NewStore(dataDir).root, RunID(sessionID))
}

// IsRunDir reports whether path is a run directory under dataDir. Session
// workspaces are the only paths that can be one, so it identifies testing sessions.
func IsRunDir(dataDir, path string) bool {
	path = filepath.Clean(path)
	return path != "." && filepath.Dir(path) == NewStore(dataDir).root && strings.HasPrefix(filepath.Base(path), "run_")
}

// reapedFile marks a run whose session has been killed, so a daemon restart
// neither forgets an expiry nor repeats it.
const reapedFile = "reaped"

// SweepExpired closes every run past its deadline: the run flips to expired (so
// every verb refuses), the session is stopped through kill, the engines close
// and the legs are cleaned. A run is marked reaped only once all of that has
// worked and no long verb is still in flight, so anything else is retried on
// the next tick. State is on disk, so a restarted daemon picks up runs the
// previous one never got to.
func (s *Service) SweepExpired(ctx context.Context, kill func(ctx context.Context, sessionID string) error, log *slog.Logger) {
	runs, err := s.store.List()
	if err != nil {
		log.Warn("litmus: could not list runs for the deadline sweep", "error", err)
		return
	}
	if s.expireUnstarted(ctx, log) {
		if runs, err = s.store.List(); err != nil {
			log.Warn("litmus: could not list runs for the deadline sweep", "error", err)
			return
		}
	}
	for _, run := range runs {
		if run.State == RunSubmitted || run.State == RunCleaned || run.State == RunAwaitingApproval || s.reaped(run.ID) {
			continue
		}
		if run.State != RunExpired && !s.clock().After(run.Deadline) {
			continue
		}
		if _, err := s.update(run.ID, func(r *Run) {
			if !r.State.Closed() {
				r.State = RunExpired
			}
		}); err != nil {
			log.Warn("litmus: could not expire a run", "run", run.ID, "error", err)
			continue
		}
		done := true
		if err := kill(ctx, run.SessionID); err != nil {
			log.Warn("litmus: could not stop the session of an expired run", "run", run.ID, "error", err)
			done = false
		}
		if s.inFlight(run.ID) { // a launch or prepare would write its leg after the cleanup below
			done = false
		} else if _, err := s.Cleanup(ctx, run.SessionID); err != nil {
			log.Warn("litmus: cleanup of an expired run failed", "run", run.ID, "error", err)
			done = false
		}
		if !done {
			continue
		}
		if err := s.store.WriteFile(run.ID, reapedFile, []byte(s.clock().UTC().Format(time.RFC3339))); err != nil {
			log.Warn("litmus: could not mark a run reaped", "run", run.ID, "error", err)
			continue
		}
		log.Info("litmus: run expired, session stopped", "run", run.ID, "reason", run.Error)
	}
}

// expireUnstarted expires the run of every tester that has not completed intake (no run.json, or one still in intake
// with no checks) once its session has been exited or idle for IdleBudget, or SpawnBudget has passed since it was
// spawned. The run keeps the reason, and the loop in SweepExpired then stops the session. It reports whether any run
// changed.
func (s *Service) expireUnstarted(ctx context.Context, log *slog.Logger) (changed bool) {
	spawns, err := s.store.spawnedSessions()
	if err != nil {
		log.Warn("litmus: could not list started testers for the deadline sweep", "error", err)
		return false
	}
	for sid, sp := range spawns {
		id, err := runID(sid)
		if err != nil {
			continue
		}
		run, err := s.store.Get(id)
		missing := errors.Is(err, fs.ErrNotExist)
		if err != nil && !missing {
			continue // unreadable: a half-written file is not a verdict
		}
		if !missing {
			checks, cerr := s.store.Checks(id)
			if cerr != nil || run.State != RunIntake || len(checks) > 0 {
				if cerr == nil {
					s.store.clearSpawned(sid) // intake is done; the run's own deadline rules from here
				}
				continue
			}
		}
		if !s.gaveUp(ctx, sid, sp) {
			continue
		}
		s.mu.Lock()
		cur, err := s.store.Get(id)
		if missing = errors.Is(err, fs.ErrNotExist); missing {
			cur = Run{ID: id, SessionID: sid, ProjectID: s.store.sessionProject(sid), Repo: sp.Repo, PRNumber: sp.PR, CreatedAt: sp.At, Deadline: sp.At.Add(RunBudget)}
		} else if err != nil || cur.State != RunIntake { // intake got in between
			s.mu.Unlock()
			continue
		}
		cur.State, cur.Error = RunExpired, ReasonNoIntake
		err = s.store.Save(cur)
		s.mu.Unlock()
		if err != nil {
			log.Warn("litmus: could not expire a tester that never ran intake", "run", id, "error", err)
			continue
		}
		s.store.clearSpawned(sid)
		changed = true
	}
	return changed
}

// gaveUp reports whether a spawned tester is past SpawnBudget, or its session is exited or has been idle for IdleBudget.
func (s *Service) gaveUp(ctx context.Context, sessionID string, sp spawned) bool {
	now := s.clock()
	if now.Sub(sp.At) > SpawnBudget {
		return true
	}
	if s.activity == nil {
		return false
	}
	state, since, found := s.activity(ctx, sessionID)
	return found && (state == "exited" || state == "idle" && now.Sub(since) > IdleBudget)
}

// inFlight reports whether a long verb of the run is still running.
func (s *Service) inFlight(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, on := range s.busy {
		if on && strings.HasPrefix(key, runID+" ") {
			return true
		}
	}
	return false
}

func (s *Service) reaped(id string) bool {
	path, err := s.store.path(id, reapedFile)
	if err != nil {
		return true
	}
	_, err = os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
