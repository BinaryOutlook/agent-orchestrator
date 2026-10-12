package litmus

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

var outcomes = []CheckState{CheckPass, CheckFail, CheckPartial, CheckSkipped}

// endGrace is how long a tester has after its run closes to print its last line before its session is stopped.
// Tests shorten it.
var endGrace = 10 * time.Second

// Service owns the run lifecycle and the check ledger. Every verb is keyed by
// the calling AO session, which owns at most one run.
type Service struct {
	mu            sync.Mutex
	store         *Store
	clock         func() time.Time
	gh            GH
	resolve       RepoResolver
	runner        Runner
	sys           sysOps
	nodeDirs      func(major string) []string
	closeEngine   func(runID string) // cleanup calls it before killing anything
	busy          map[string]bool    // long verbs in flight, by run and verb
	engines       map[string]*Engine // by run/leg
	seqs          map[string]int     // receipts handed out, by run
	engExec       execFn
	media         Media           // ffmpeg, behind an interface so tests never run it
	spawn         Spawner         // launches testing workers; nil until the daemon wires it
	report        Reporter        // posts the session's `done` report after a submit; nil until the daemon wires it
	kill          Killer          // stops the tester's session after a submit; nil until the daemon wires it
	activity      SessionActivity // reads a tester's activity for the sweep; nil until the daemon wires it
	binary        string          // the agent-browser binary; empty when it was not found (reported on first use)
	login         string          // the gh token's login, read by the first comment post
	evidenceMu    sync.Mutex      // one publish at a time: runs of a repo share its evidence clone
	testCap       time.Duration   // longest a `test` verb may run; tests shorten it
	keepRuns      int             // retention after every run: finished run dirs kept
	keepCheckouts int             // retention after every run: finished runs whose checkouts are kept
}

// New builds a service over store. A nil clock uses time.Now.
func New(store *Store, clock func() time.Time) *Service {
	if clock == nil {
		clock = time.Now
	}
	s := &Service{store: store, clock: clock, gh: execGH{}, runner: execRunner{}, sys: realSys(), nodeDirs: defaultNodeDirs, busy: map[string]bool{},
		engines: map[string]*Engine{}, seqs: map[string]int{}, engExec: realExec,
		media: execMedia{}, testCap: testCap, keepRuns: DefaultKeepRuns, keepCheckouts: DefaultKeepCheckouts}
	s.closeEngine = s.closeEngines
	if exe, err := os.Executable(); err == nil {
		s.binary, _ = resolveBinary(os.Getenv("AO_AGENT_BROWSER_PATH"), exe)
	}
	return s
}

// autoPrune is the retention pass a closed run ends with. It writes the summary, and any skip lines, to the run's
// comment.log when something was deleted or skipped, and never fails the run.
func (s *Service) autoPrune(ctx context.Context, run Run) {
	rep, err := s.prune(ctx, s.keepRuns, s.keepCheckouts, false, run.ID) // this run's dir is kept whatever keepRuns says
	if err != nil {
		s.commentLogf(run, "prune failed: %v", err)
		return
	}
	skips := slices.DeleteFunc(slices.Clone(rep.Lines), func(l string) bool { return !strings.HasPrefix(l, "skipped") })
	if len(rep.Checkouts)+len(rep.Runs) == 0 && len(skips) == 0 {
		return // nothing deleted and nothing skipped: nothing to say
	}
	for _, l := range skips { // a skipped path or checkout is why less was deleted than expected
		s.commentLogf(run, "%s", l)
	}
	s.commentLogf(run, "%s", rep.Summary())
}

// WithGH swaps the gh runner, so tests never reach the network.
func (s *Service) WithGH(gh GH) *Service {
	s.gh = gh
	return s
}

// WithRepos sets how owner/repo is mapped to the local checkout that clones are made from.
func (s *Service) WithRepos(resolve RepoResolver) *Service {
	s.resolve = resolve
	return s
}

// Reporter posts the worker report `done` for a session, as `ao report --done` would.
type Reporter func(ctx context.Context, sessionID, note string) error

// WithReporter sets how a submitted run reports its session done.
func (s *Service) WithReporter(r Reporter) *Service {
	s.report = r
	return s
}

// Killer stops a tester's AO session. The deadline sweep and a finished run end the tester through the same one.
type Killer func(ctx context.Context, sessionID string) error

// WithKiller sets how a submitted run's tester is stopped, endGrace after its done report.
func (s *Service) WithKiller(k Killer) *Service {
	s.kill = k
	return s
}

// SessionActivity reads a session's activity state ("idle", "exited", ...) and when it last changed. found is false
// when the session is unknown.
type SessionActivity func(ctx context.Context, sessionID string) (state string, since time.Time, found bool)

// WithActivity sets how the sweep reads a tester's activity, to reap one that never ran intake. Without it only the
// time since spawn applies.
func (s *Service) WithActivity(a SessionActivity) *Service {
	s.activity = a
	return s
}

// authorTaboo is the tester's own vocabulary: a check id, head, base, leg, snapshot, ref or receipt.
var authorTaboo = regexp.MustCompile(`(?i)\b(c\d+|head|base|legs?|snapshots?|refs?|receipts?)\b`)

// forAuthor refuses tester text that would reach the PR author in the tester's words: it names a check or one of the
// words above, or runs past 240 characters. The comment says "the PR build" and "main".
func forAuthor(text string) error {
	if len([]rune(text)) > 240 || authorTaboo.MatchString(text) {
		return invalid("TESTING_NOTE_FOR_AUTHOR", "write this for the PR author: say 'the PR build' and 'main', name what you saw, no check ids, two sentences at most")
	}
	return nil
}

func invalid(code, msg string) error  { return apierr.Invalid(code, msg, nil) }
func conflict(code, msg string) error { return apierr.Conflict(code, msg, nil) }

// runID names the one run a session may own, so a second intake cannot reset
// the 25 minute clock and every verb finds its run without a scan.
func runID(sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", invalid("SESSION_ID_REQUIRED", "sessionId is required")
	}
	return "run_" + sessionID, nil
}

// Status returns the run and its checks.
func (s *Service) Status(_ context.Context, sessionID string) (RunStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.load(sessionID)
	if err != nil {
		return RunStatus{}, err
	}
	checks, err := s.store.Checks(run.ID)
	return RunStatus{Run: run, Checks: append([]Check{}, checks...)}, err
}

// maxChecks bounds a plan; minChecks must exist before the PR build is launched.
const (
	minChecks = 2
	maxChecks = 12 // 6 per build: every head check needs a same-named base check
)

// CheckAdd appends a pending check with the observable the worker expects. leg is
// "base", "head" or empty (the check acts on whichever leg the worker drives).
func (s *Service) CheckAdd(_ context.Context, sessionID, name, expect, leg string) (Check, error) {
	if leg != "" && leg != "base" && leg != "head" {
		return Check{}, invalid("TESTING_LEG_INVALID", "leg must be base or head")
	}
	name, expect = strings.TrimSpace(name), strings.TrimSpace(expect)
	if name == "" || expect == "" {
		return Check{}, invalid("TESTING_CHECK_INVALID", "a check needs a name and an expected observable")
	}
	if err := forAuthor(expect); err != nil {
		return Check{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.load(sessionID)
	if err != nil {
		return Check{}, err
	}
	checks, err := s.store.Checks(run.ID)
	if err != nil {
		return Check{}, err
	}
	if len(checks) >= maxChecks {
		return Check{}, conflict("TESTING_CHECK_LIMIT", fmt.Sprintf("a run has at most %d checks; fold this one into an existing check", maxChecks))
	}
	check := Check{ID: "c" + strconv.Itoa(len(checks)+1), Name: name, Expect: expect, State: CheckPending, Leg: leg}
	return check, s.store.SaveChecks(run.ID, append(checks, check))
}

// CheckStart opens a check so receipts attach to it. One check runs at a time.
func (s *Service) CheckStart(_ context.Context, sessionID, id string) (Check, error) {
	return s.updateCheck(sessionID, id, func(c *Check, all []Check) error {
		for _, other := range all {
			if other.State == CheckRunning {
				return conflict("TESTING_CHECK_STATE", "check "+other.ID+" is still running; end it first")
			}
		}
		if c.State != CheckPending {
			return conflict("TESTING_CHECK_STATE", fmt.Sprintf("check %s is %s; only a pending check can start", c.ID, c.State))
		}
		c.State = CheckRunning
		return nil
	})
}

// CheckEnd closes a running check with its outcome (pass, fail, partial or skipped).
func (s *Service) CheckEnd(_ context.Context, sessionID, id string, result CheckState, note string) (Check, error) {
	if !slices.Contains(outcomes, result) {
		return Check{}, invalid("TESTING_CHECK_RESULT_INVALID", "result must be pass, fail, partial or skipped")
	}
	if err := forAuthor(note); err != nil {
		return Check{}, err
	}
	return s.updateCheck(sessionID, id, func(c *Check, _ []Check) error {
		if c.State != CheckRunning {
			return conflict("TESTING_CHECK_STATE", fmt.Sprintf("check %s is %s; only a running check can end", c.ID, c.State))
		}
		if result != CheckSkipped && !slices.ContainsFunc(c.Receipts, func(r Receipt) bool { return r.onLeg() == c.onLeg() }) {
			return conflict("TESTING_CHECK_NO_RECEIPT", fmt.Sprintf("check %s has no receipt on the %s leg; act on it (click, fill, press, ...) with --leg %s first", c.ID, c.onLeg(), c.onLeg()))
		}
		c.State, c.Note = result, strings.TrimSpace(note)
		return nil
	})
}

// begin loads the session's open run and marks verb in flight, so a second call
// (a retry after a CLI timeout) gets a conflict instead of racing the first.
func (s *Service) begin(sessionID, verb string) (Run, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.load(sessionID)
	if err != nil {
		return Run{}, nil, err
	}
	done, err := s.markBusy(run.ID, verb)
	return run, done, err
}

// markBusy marks verb of run in flight and returns the func that clears it. The mutating verbs exclude each other
// across keys: submit and cleanup refuse while any launch runs (they would stop a recording the launch has not
// stored yet), and a launch refuses while a submit or cleanup runs. Callers hold s.mu.
func (s *Service) markBusy(runID, verb string) (func(), error) {
	key := runID + " " + verb
	if s.busy[key] {
		return nil, conflict("TESTING_BUSY", verb+" is already in progress for "+runID+"; poll `ao testing status`")
	}
	closing, launching := verb == "submit" || verb == "cleanup", strings.HasPrefix(verb, "launch ")
	for k, on := range s.busy {
		other, ours := strings.CutPrefix(k, runID+" ")
		if on && ours && ((closing && strings.HasPrefix(other, "launch ")) || (launching && (other == "submit" || other == "cleanup"))) {
			return nil, conflict("TESTING_RUN_BUSY", fmt.Sprintf("%s is refused while %s is in progress for %s; wait for it to finish", verb, other, runID))
		}
	}
	s.busy[key] = true
	return func() { s.mu.Lock(); delete(s.busy, key); s.mu.Unlock() }, nil
}

// update re-reads the run, applies fn and saves it: long verbs run unlocked and merge their result here.
func (s *Service) update(id string, fn func(*Run)) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.store.Get(id)
	if err != nil {
		return Run{}, err
	}
	fn(&run)
	return run, s.store.Save(run)
}

// load returns the session's run, flipping it to expired once the deadline has
// passed, and refuses a submitted, expired or cleaned run: every verb shares
// that refusal. Callers hold s.mu.
func (s *Service) load(sessionID string) (Run, error) {
	id, err := runID(sessionID)
	if err != nil {
		return Run{}, err
	}
	run, err := s.store.Get(id)
	if errors.Is(err, fs.ErrNotExist) {
		return Run{}, apierr.NotFound("TESTING_RUN_NOT_FOUND", "no testing run for this session; start with `ao testing intake <pr-url>`")
	}
	if err != nil {
		return Run{}, err
	}
	if !run.State.Closed() && s.clock().After(run.Deadline) {
		run.State = RunExpired
		if err := s.store.Save(run); err != nil {
			return Run{}, err
		}
	}
	if run.State.Closed() {
		why := ""
		if run.State == RunExpired && run.Error != "" {
			why = ": " + run.Error
		}
		return Run{}, conflict("TESTING_RUN_CLOSED", fmt.Sprintf("run %s is %s%s; testing verbs are refused", run.ID, run.State, why))
	}
	return run, nil
}

// updateCheck applies fn to one check and persists the ledger.
func (s *Service) updateCheck(sessionID, id string, fn func(c *Check, all []Check) error) (Check, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.load(sessionID)
	if err != nil {
		return Check{}, err
	}
	checks, err := s.store.Checks(run.ID)
	if err != nil {
		return Check{}, err
	}
	for i := range checks {
		if checks[i].ID != id {
			continue
		}
		if err := fn(&checks[i], checks); err != nil {
			return Check{}, err
		}
		return checks[i], s.store.SaveChecks(run.ID, checks)
	}
	return Check{}, apierr.NotFound("TESTING_CHECK_NOT_FOUND", "no check "+strconv.Quote(id))
}
