package litmus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pruneFixture lays out three checkouts, four runs and an evidence clone under a temp data dir.
// c1 is referenced by two old finished runs, c2 by the recent finished run, c3 by a running run.
func pruneFixture(t *testing.T) (svc *Service, root string, recent Run) {
	t.Helper()
	svc = New(NewStore(t.TempDir()), nil)
	root = filepath.Dir(svc.store.root)
	c1, c2, c3 := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)
	for _, sha := range []string{c1, c2, c3} {
		dir := filepath.Join(root, "repos", "aoagents__agent-orchestrator", sha)
		writeFile(t, filepath.Join(dir, "node_modules", "big.bin"), strings.Repeat("x", 1000))
		old := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(dir, old, old); err != nil { // a checkout nobody used lately
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "evidence", "aoagents__agent-orchestrator", "runs", "1", "a.png"), "png")
	day := 24 * time.Hour
	base := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	for _, r := range []Run{
		{ID: "run_old1", State: RunCleaned, CreatedAt: base.Add(-4 * day), HeadSHA: c1, BaseSHA: c1},
		{ID: "run_old2", State: RunSubmitted, CreatedAt: base.Add(-3 * day), HeadSHA: c1, BaseSHA: c1},
		{ID: "run_live", State: RunRunning, CreatedAt: base.Add(-2 * day), HeadSHA: c3, BaseSHA: c3},
		{ID: "run_recent", State: RunSubmitted, CreatedAt: base.Add(-day), HeadSHA: c2, BaseSHA: c2},
	} {
		r.Repo = "aoagents/agent-orchestrator"
		if err := svc.store.Save(r); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(svc.store.root, r.ID, "verdict.md"), "v")
		if r.ID == "run_recent" {
			recent = r
		}
	}
	return svc, root, recent
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pathExists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestPruneDryRunDeletesNothing(t *testing.T) {
	svc, root, _ := pruneFixture(t)
	rep, err := svc.PruneDryRun(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || len(rep.Checkouts) != 1 || len(rep.Runs) != 2 || rep.BytesFreed < 1000 {
		t.Fatalf("report = %+v", rep)
	}
	if !strings.HasPrefix(rep.Lines[0], "would remove checkout repos/") || !strings.HasPrefix(rep.Lines[len(rep.Lines)-1], "would prune: 1 checkouts, 2 runs") {
		t.Fatalf("lines = %q", rep.Lines)
	}
	for _, p := range append(append([]string{}, rep.Checkouts...), rep.Runs...) {
		if !pathExists(filepath.Join(root, filepath.FromSlash(p))) {
			t.Fatalf("dry run deleted %s", p)
		}
	}
}

func TestPruneRemovesExactlyTheStaleDirs(t *testing.T) {
	svc, root, _ := pruneFixture(t)
	rep, err := svc.Prune(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repos", "aoagents__agent-orchestrator")
	for path, want := range map[string]bool{
		filepath.Join(repo, strings.Repeat("1", 40)):                                          false, // only old finished runs
		filepath.Join(repo, strings.Repeat("2", 40)):                                          true,  // the newest finished run
		filepath.Join(repo, strings.Repeat("3", 40)):                                          true,  // a running run, however old
		filepath.Join(svc.store.root, "run_old1"):                                             false,
		filepath.Join(svc.store.root, "run_old2"):                                             false,
		filepath.Join(svc.store.root, "run_live"):                                             true,
		filepath.Join(svc.store.root, "run_recent"):                                           true,
		filepath.Join(root, "evidence", "aoagents__agent-orchestrator", "runs", "1", "a.png"): true,
	} {
		if got := pathExists(path); got != want {
			t.Errorf("%s exists = %v, want %v", strings.TrimPrefix(path, root), got, want)
		}
	}
	wantRel := "repos/aoagents__agent-orchestrator/" + strings.Repeat("1", 40)
	if len(rep.Checkouts) != 1 || rep.Checkouts[0] != wantRel || len(rep.Runs) != 2 || rep.BytesFreed < 1000 {
		t.Fatalf("report = %+v", rep)
	}
	if got := rep.Lines[len(rep.Lines)-1]; got != "pruned: 1 checkouts, 2 runs, 0.0 GB freed" {
		t.Fatalf("summary = %q", got)
	}
}

func TestPruneKeepsEveryCheckoutWhileARunIsUnfinished(t *testing.T) {
	svc, root, _ := pruneFixture(t)
	if _, err := svc.Prune(ctx, 0, 0); err != nil { // keep nothing that is not needed
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repos", "aoagents__agent-orchestrator")
	if !pathExists(filepath.Join(repo, strings.Repeat("3", 40))) || !pathExists(filepath.Join(svc.store.root, "run_live")) {
		t.Fatal("the running run lost its checkout or its dir")
	}
	if pathExists(filepath.Join(repo, strings.Repeat("1", 40))) || pathExists(filepath.Join(repo, strings.Repeat("2", 40))) {
		t.Fatal("finished runs' checkouts should go at keep 0")
	}
}

func TestPruneRefusesPathsOutsideTheDataDir(t *testing.T) {
	svc, root, _ := pruneFixture(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, strings.Repeat("9", 40), "keep.txt"), "keep")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(outside, strings.Repeat("9", 40)), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "repos", "evil__repo")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Prune(ctx, 1, 1); err == nil {
		t.Fatal("want an error for the symlinked repo dir")
	}
	if !pathExists(filepath.Join(outside, strings.Repeat("9", 40), "keep.txt")) {
		t.Fatal("deleted a dir outside the data dir")
	}
}

func TestPruneRejectsNegativeKeep(t *testing.T) {
	svc, _, _ := pruneFixture(t)
	is(t, "TESTING_PRUNE_INVALID")(svc.Prune(ctx, -1, 2))
}

func TestAutoPruneLogsOneLineToCommentLog(t *testing.T) {
	svc, _, recent := pruneFixture(t)
	svc.keepRuns, svc.keepCheckouts = 1, 1
	svc.autoPrune(ctx, recent)
	log, err := os.ReadFile(filepath.Join(svc.store.root, recent.ID, "comment.log"))
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(log)), "pruned: 1 checkouts, 2 runs, 0.0 GB freed") {
		t.Fatalf("comment.log = %q, %v", log, err)
	}
}

func TestPruneKeepsACheckoutAFreshRunPinnedAfterThePlan(t *testing.T) {
	svc, root, _ := pruneFixture(t)
	c1 := filepath.Join(root, "repos", "aoagents__agent-orchestrator", strings.Repeat("1", 40))
	pruneSeam = func() { // a run starts between the plan and the delete and reuses c1
		if err := svc.store.Save(Run{ID: "run_new", State: RunIntake, CreatedAt: time.Now(), HeadSHA: strings.Repeat("1", 40)}); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { pruneSeam = nil })
	rep, err := svc.Prune(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !pathExists(c1) || len(rep.Checkouts) != 0 {
		t.Fatalf("c1 was deleted under a run that started after the plan: %+v", rep)
	}
}

func TestPruneKeepsRecentlyUsedCheckouts(t *testing.T) {
	svc, root, _ := pruneFixture(t)
	c1 := filepath.Join(root, "repos", "aoagents__agent-orchestrator", strings.Repeat("1", 40))
	now := time.Now()
	if err := os.Chtimes(c1, now, now); err != nil {
		t.Fatal(err)
	}
	if rep, err := svc.Prune(ctx, 1, 1); err != nil || len(rep.Checkouts) != 0 || !pathExists(c1) {
		t.Fatalf("a checkout used a minute ago was removed: %+v %v", rep, err)
	}
	writeFile(t, filepath.Join(c1, "manifest.json"), `{"usedAt":"`+now.Add(-time.Hour).UTC().Format(time.RFC3339)+`"}`)
	if rep, err := svc.Prune(ctx, 1, 1); err != nil || len(rep.Checkouts) != 1 {
		t.Fatalf("manifest usedAt an hour ago should allow removal: %+v %v", rep, err)
	}
}

func TestPruneSkipsCheckoutsWhileARunJSONIsUnreadable(t *testing.T) {
	svc, root, _ := pruneFixture(t)
	writeFile(t, filepath.Join(svc.store.root, "run_broken", "run.json"), "{not json")
	rep, err := svc.Prune(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Checkouts) != 0 || !pathExists(filepath.Join(root, "repos", "aoagents__agent-orchestrator", strings.Repeat("1", 40))) {
		t.Fatalf("checkouts pruned with an unreadable run: %+v", rep)
	}
	if !pathExists(filepath.Join(svc.store.root, "run_broken")) || len(rep.Runs) != 2 || !strings.Contains(strings.Join(rep.Lines, "\n"), "skipped checkouts: 1 run dirs have no readable run.json (run_broken)") {
		t.Fatalf("report = %+v", rep)
	}
}

func TestAutoPruneKeepsTheCurrentRunAtKeepRunsZero(t *testing.T) {
	svc, _, recent := pruneFixture(t)
	svc.keepRuns, svc.keepCheckouts = 0, 0
	svc.autoPrune(ctx, recent)
	for id, want := range map[string]bool{"run_recent": true, "run_live": true, "run_old1": false, "run_old2": false} {
		if got := pathExists(filepath.Join(svc.store.root, id)); got != want {
			t.Errorf("%s exists = %v, want %v", id, got, want)
		}
	}
	if log, err := os.ReadFile(filepath.Join(svc.store.root, recent.ID, "comment.log")); err != nil || !strings.Contains(string(log), "pruned: ") {
		t.Fatalf("comment.log = %q, %v", log, err)
	}
}

func TestPruneKeepRunsCountsOnlyFinishedRuns(t *testing.T) {
	svc, _, _ := pruneFixture(t)
	rep, err := svc.Prune(ctx, 2, 2) // finished: recent, old2 kept; the running run does not use a slot
	if err != nil || len(rep.Runs) != 1 || rep.Runs[0] != "runs/run_old1" {
		t.Fatalf("report = %+v err=%v", rep, err)
	}
}

func TestAutoPruneIsSilentOnlyWhenNothingWasDeletedOrSkipped(t *testing.T) {
	svc, _, recent := pruneFixture(t)
	svc.keepRuns, svc.keepCheckouts = 100, 100
	logPath := filepath.Join(svc.store.root, recent.ID, "comment.log")
	svc.autoPrune(ctx, recent)
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("logged although nothing was deleted or skipped")
	}
	writeFile(t, filepath.Join(svc.store.root, "run_broken", "run.json"), "{not json")
	svc.autoPrune(ctx, recent)
	log, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(log), "skipped checkouts: 1 run dirs have no readable run.json (run_broken)") || !strings.Contains(string(log), "pruned: 0 checkouts, 0 runs") {
		t.Fatalf("comment.log = %q, %v", log, err)
	}
}
