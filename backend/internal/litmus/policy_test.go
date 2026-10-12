package litmus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestToolPolicyListsAndRunDir(t *testing.T) {
	allowed, denied := ToolPolicy()
	for _, a := range []string{"Bash", "Read", "Edit", "Write", "Grep", "Glob", "WebFetch", "WebSearch"} {
		if !slices.Contains(allowed, a) {
			t.Errorf("%s is not allowed", a)
		}
	}
	for _, d := range []string{"Bash(git push:*)", "Bash(gh pr comment:*)", "Bash(gh pr review:*)", "Bash(gh api:*)", "Bash(gh pr update-branch:*)", "Bash(ao testing start:*)", "Bash(ao spawn:*)", "Bash(ao session:*)", "Bash(ao send:*)", "Bash(ao pr:*)", "Bash(ao stop:*)"} {
		if !slices.Contains(denied, d) {
			t.Errorf("%s is not denied", d)
		}
	}
	for _, d := range denied {
		if !strings.HasPrefix(d, "Bash(") {
			t.Errorf("a whole tool is denied: %s", d)
		}
	}
	allowed[0], denied[0] = "x", "x" // callers get copies
	if a, d := ToolPolicy(); a[0] == "x" || d[0] == "x" {
		t.Fatal("ToolPolicy shares its backing lists")
	}

	dir := RunDir("/data", "proj-7")
	if dir != filepath.Join("/data", "litmus", "runs", "run_proj-7") || !IsRunDir("/data", dir) {
		t.Fatalf("run dir %q", dir)
	}
	for _, not := range []string{"", "/data/litmus/runs", "/data/litmus/runs/run_a/receipts", "/data/worktrees/run_a", "/other/litmus/runs/run_a", "/data/litmus/runs/x"} {
		if IsRunDir("/data", not) {
			t.Errorf("IsRunDir(%q) = true", not)
		}
	}
}

// sweep runs one deadline sweep and returns the sessions kill was asked to stop.
func sweep(svc *Service, killErr error) (killed []string) {
	svc.SweepExpired(context.Background(), func(_ context.Context, id string) error {
		killed = append(killed, id)
		return killErr
	}, quiet)
	slices.Sort(killed) // List order among equal creation times is not defined
	return killed
}

func TestSweepExpiredTransitionsAndRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	svc := New(NewStore(dir), func() time.Time { return now }).WithGH(fixtureGH(t))
	for _, sid := range []string{"old", "lazy", "done"} {
		is(t, "")(svc.Intake(ctx, sid, prURL))
	}
	now = now.Add(time.Minute)
	is(t, "")(svc.Intake(ctx, "young", prURL)) // its deadline is a minute after the others
	is(t, "")(nil, svc.store.Save(Run{ID: "run_gone", SessionID: "gone", State: RunSubmitted, Deadline: now.Add(-time.Hour)}))
	is(t, "")(svc.update("run_done", func(r *Run) { r.State = RunSubmitted }))

	if got := sweep(svc, nil); got != nil {
		t.Fatalf("killed %v before any deadline", got)
	}

	now = now.Add(RunBudget - time.Minute + time.Second) // old, lazy and done are past the deadline, young is not
	is(t, "TESTING_RUN_CLOSED")(svc.Status(ctx, "lazy")) // a verb flips lazy to expired before the sweep sees it
	if got := sweep(svc, errors.New("kill failed")); !slices.Equal(got, []string{"lazy", "old"}) {
		t.Fatalf("killed %v, want lazy and old (a submitted run is left alone)", got)
	}
	if got := sweep(svc, nil); !slices.Equal(got, []string{"lazy", "old"}) { // the failed kill is retried
		t.Fatalf("retry killed %v", got)
	}
	if got := sweep(svc, nil); got != nil { // reaped runs are not touched again
		t.Fatalf("killed %v after reaping", got)
	}
	states := map[string]RunState{}
	runs, _ := svc.store.List()
	for _, r := range runs {
		states[r.SessionID] = r.State
	}
	if states["old"] != RunExpired || states["lazy"] != RunExpired || states["young"] != RunIntake || states["done"] != RunSubmitted {
		t.Fatalf("states = %v", states)
	}
	is(t, "TESTING_RUN_CLOSED")(svc.CheckAdd(ctx, "old", "late", "x", "")) // every verb refuses once expired
	is(t, "TESTING_RUN_CLOSED")(svc.Submit(ctx, "old", "", ""))
	is(t, "")(svc.CheckAdd(ctx, "young", "still open", "x", ""))

	// A new daemon over the same data dir knows what was reaped from disk alone.
	again := New(NewStore(dir), func() time.Time { return now.Add(time.Hour) }).WithGH(fixtureGH(t))
	if got := sweep(again, nil); !slices.Equal(got, []string{"young"}) {
		t.Fatalf("restart killed %v, want only young", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "litmus", "runs", "run_young", reapedFile)); err != nil {
		t.Fatal(err)
	}
}

func TestSweepWaitsForInFlightVerbBeforeReaping(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	svc := New(NewStore(dir), func() time.Time { return now }).WithGH(fixtureGH(t))
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	now = now.Add(RunBudget + time.Second)

	svc.busy["run_s1 launch"] = busyVerb{since: now, done: make(chan struct{})} // a launch is still building when the deadline passes
	if got := sweep(svc, nil); !slices.Equal(got, []string{"s1"}) {
		t.Fatalf("the session of an expired run is stopped at once, killed %v", got)
	}
	if run, _ := svc.store.Get("run_s1"); run.State != RunExpired || svc.reaped("run_s1") {
		t.Fatalf("state %s reaped %t while the launch was still running", run.State, svc.reaped("run_s1"))
	}
	delete(svc.busy, "run_s1 launch")
	if got := sweep(svc, nil); !slices.Equal(got, []string{"s1"}) || !svc.reaped("run_s1") {
		t.Fatalf("after the launch: killed %v, reaped %t", got, svc.reaped("run_s1"))
	}
}

// spawnTester starts a tester through Start with a spawner that returns sid.
func spawnTester(t *testing.T, svc *Service, sid string) {
	t.Helper()
	svc.WithSpawner(func(context.Context, SpawnRequest) (string, error) { return sid, nil })
	is(t, "")(svc.Start(ctx, StartRequest{PRURL: prURL}))
}

func TestSweepReapsATesterThatNeverRanIntake(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	act := map[string]string{} // session -> activity state
	since := now
	svc := New(NewStore(dir), func() time.Time { return now }).WithGH(fixtureGH(t)).
		WithActivity(func(_ context.Context, id string) (string, time.Time, bool) {
			st, ok := act[id]
			return st, since, ok
		})
	for _, sid := range []string{"idle", "gone", "slow", "worked", "busy"} {
		spawnTester(t, svc, sid)
	}
	is(t, "")(svc.Intake(ctx, "worked", prURL)) // ran intake and planned a check
	is(t, "")(svc.CheckAdd(ctx, "worked", "a check", "x", ""))
	act["idle"], act["gone"], act["worked"], act["busy"] = "idle", "active", "idle", "active"

	now = now.Add(time.Minute)
	if got := sweep(svc, nil); got != nil {
		t.Fatalf("killed %v a minute in", got)
	}
	act["gone"] = "exited"                     // exited counts at once
	since = now.Add(-IdleBudget - time.Second) // idle has now lasted over 2 minutes
	now = now.Add(time.Minute)
	if got := sweep(svc, nil); !slices.Equal(got, []string{"gone", "idle"}) {
		t.Fatalf("killed %v, want gone (exited) and idle (idle over 2 minutes); worked has checks, busy is active", got)
	}
	run, err := svc.store.Get("run_idle")
	if err != nil || run.State != RunExpired || run.Error != ReasonNoIntake || run.Repo != "aoagents/agent-orchestrator" || run.PRNumber != 6394 {
		t.Fatalf("run.json = %+v, %v", run, err)
	}
	_, err = svc.Status(ctx, "idle")
	if !strings.Contains(fmt.Sprint(err), ReasonNoIntake) {
		t.Fatalf("status does not explain: %v", err)
	}
	if got := sweep(svc, nil); got != nil { // reaped once, not again
		t.Fatalf("killed %v again", got)
	}

	// Past 5 minutes from spawn the rest go whatever their session says, except a run that has checks.
	now = now.Add(SpawnBudget)
	if got := sweep(svc, nil); !slices.Equal(got, []string{"busy", "slow"}) {
		t.Fatalf("killed %v", got)
	}
	if run, _ := svc.store.Get("run_worked"); run.Error == ReasonNoIntake {
		t.Fatalf("a tester with checks was reaped as never having run intake: %+v", run)
	}
	if run, _ := svc.store.Get("run_slow"); run.State != RunExpired || run.Error != ReasonNoIntake {
		t.Fatalf("slow = %+v", run)
	}
}

func TestSweepReapsAnIntakeOnlyRunAfterSpawnBudgetWithoutActivitySeam(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	svc := New(NewStore(t.TempDir()), func() time.Time { return now }).WithGH(fixtureGH(t))
	spawnTester(t, svc, "s1")
	is(t, "")(svc.Intake(ctx, "s1", prURL)) // intake ran, no checks followed
	now = now.Add(SpawnBudget - time.Second)
	if got := sweep(svc, nil); got != nil {
		t.Fatalf("killed %v before 5 minutes", got)
	}
	now = now.Add(2 * time.Second)
	if got := sweep(svc, nil); !slices.Equal(got, []string{"s1"}) {
		t.Fatalf("killed %v", got)
	}
	if run, _ := svc.store.Get("run_s1"); run.State != RunExpired || run.Error != ReasonNoIntake || run.HeadSHA == "" {
		t.Fatalf("an existing intake run lost its pins or missed the reason: %+v", run)
	}
}

func TestSweepDiscardsARunThatWaitedAWeekForApproval(t *testing.T) {
	svc, runDir, g := askSvc(t, true)
	_, err := svc.Submit(ctx, "s1", "", "")
	is(t, "")(nil, err)
	run, _ := svc.store.Get("run_s1")

	svc.clock = func() time.Time { return run.CreatedAt.Add(AwaitingTTL) }
	if killed := sweep(svc, nil); len(killed) != 0 {
		t.Fatalf("killed %v", killed)
	}
	if got, _ := svc.store.Get("run_s1"); got.State != RunAwaitingApproval {
		t.Fatalf("a run exactly at the limit was discarded: %s", got.State)
	}

	svc.clock = func() time.Time { return run.CreatedAt.Add(AwaitingTTL + time.Second) }
	sweep(svc, nil)
	got, _ := svc.store.Get("run_s1")
	if got.State != RunCleaned || got.CommentState != CommentDiscarded || len(g.calls) != 0 {
		t.Fatalf("after a week: %s %q, gh calls %q", got.State, got.CommentState, g.calls)
	}
	log, _ := os.ReadFile(filepath.Join(runDir, "comment.log"))
	if !strings.Contains(string(log), "discarded: nobody approved or discarded this comment within 7 days") {
		t.Fatalf("comment.log = %q", log)
	}
	is(t, "TESTING_NOT_AWAITING")(svc.Post(ctx, "run_s1"))
}
