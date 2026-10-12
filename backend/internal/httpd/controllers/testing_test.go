package controllers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
)

func testingServer(t *testing.T, deps httpd.APIDeps) func(method, path, body string, status int, want ...string) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, deps, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return func(method, path, body string, status int, want ...string) {
		t.Helper()
		got, code, _ := doRequest(t, srv, method, "/api/v1/testing"+path, body)
		if code != status {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, code, status, got)
		}
		for _, w := range want {
			if !strings.Contains(string(got), w) {
				t.Fatalf("%s %s body %s lacks %q", method, path, got, w)
			}
		}
	}
}

func TestTestingAPIStatusAndSubmit(t *testing.T) {
	store := litmus.NewStore(t.TempDir())
	do := testingServer(t, httpd.APIDeps{Testing: litmus.New(store, nil).WithGH(recordedGH{t: t})})
	do("GET", "/status?sessionId=ao-1", "", http.StatusNotFound, "TESTING_RUN_NOT_FOUND")
	do("POST", "/intake", `{"sessionId":"ao-1","prUrl":"https://github.com/aoagents/agent-orchestrator/pull/6394"}`, http.StatusOK, `"state":"intake"`, `"prNumber":6394`, `"comment":"post"`)
	do("GET", "/status?sessionId=ao-1", "", http.StatusOK, `"checks":[]`)
	do("POST", "/checks", `{"sessionId":"ao-1","name":"model menu","expect":"last model reachable"}`, http.StatusOK, `"id":"c1"`)
	do("POST", "/checks/c1/start", `{"sessionId":"ao-1"}`, http.StatusOK, `"state":"running"`)
	do("POST", "/checks/c1/end", `{"sessionId":"ao-1","result":"maybe"}`, http.StatusBadRequest, "TESTING_CHECK_RESULT_INVALID")
	do("POST", "/checks/c1/end", `{"sessionId":"ao-1","result":"pass"}`, http.StatusConflict, "TESTING_CHECK_NO_RECEIPT") // nothing was driven
	do("POST", "/submit", `{"sessionId":"ao-1"}`, http.StatusBadRequest, "TESTING_CHECKS_INCOMPLETE", "c1 model menu")
	checks, err := store.Checks("run_ao-1") // receipts normally arrive from the drive stage
	if err != nil {
		t.Fatal(err)
	}
	checks[0].Receipts = []litmus.Receipt{{Seq: 1, Action: "click"}}
	if err := store.SaveChecks("run_ao-1", checks); err != nil {
		t.Fatal(err)
	}
	do("POST", "/checks/c1/end", `{"sessionId":"ao-1","result":"pass"}`, http.StatusOK, `"state":"pass"`)
	do("POST", "/drive", `{"sessionId":"ao-1","action":"click","args":["e12"]}`, http.StatusConflict, "TESTING_RUN_NOT_RUNNING")
	do("POST", "/submit", `{"sessionId":"ao-1"}`, http.StatusOK, `"overall":"partial"`, "base-vs-head difference was not shown")
	do("POST", "/drive", `{"sessionId":"ao-1","action":"click","args":["e12"]}`, http.StatusConflict, "TESTING_RUN_CLOSED")
	do("POST", "/intake", `{"sessionId":"ao-1","unknown":true}`, http.StatusBadRequest, "INVALID_JSON")
}

func TestTestingAPISubmitBlocked(t *testing.T) {
	do := testingServer(t, httpd.APIDeps{Testing: litmus.New(litmus.NewStore(t.TempDir()), nil).WithGH(recordedGH{t: t})})
	do("POST", "/intake", `{"sessionId":"ao-1","prUrl":"https://github.com/aoagents/agent-orchestrator/pull/6394"}`, http.StatusOK)
	do("POST", "/submit", `{"sessionId":"ao-1","blocked":true,"reason":"  "}`, http.StatusBadRequest, "TESTING_REASON_REQUIRED")
	do("POST", "/submit", `{"sessionId":"ao-1","blocked":true,"reason":"prepare refused"}`, http.StatusOK, `"overall":"blocked"`, `"reasons":["prepare refused"]`)
	do("POST", "/submit", `{"sessionId":"ao-1","blocked":true,"reason":"again"}`, http.StatusConflict, "TESTING_RUN_CLOSED")
}

func TestTestingAPICheckLegAndCompareRoute(t *testing.T) {
	do := testingServer(t, httpd.APIDeps{Testing: litmus.New(litmus.NewStore(t.TempDir()), nil).WithGH(recordedGH{t: t})})
	do("POST", "/intake", `{"sessionId":"ao-1","prUrl":"https://github.com/aoagents/agent-orchestrator/pull/6394"}`, http.StatusOK)
	do("POST", "/checks", `{"sessionId":"ao-1","name":"menu","expect":"opens","leg":"base"}`, http.StatusOK, `"id":"c1"`, `"leg":"base"`, `"name":"menu"`)
	do("POST", "/checks", `{"sessionId":"ao-1","name":"menu","expect":"opens","leg":"main"}`, http.StatusBadRequest, "TESTING_LEG_INVALID")
	do("POST", "/checks/c9/compare", `{"sessionId":"ao-1"}`, http.StatusNotFound, "TESTING_CHECK_NOT_FOUND")
	do("POST", "/checks/c1/compare", `{"sessionId":"ao-1"}`, http.StatusConflict, "TESTING_COMPARE_INCOMPLETE") // no after-shots
}

func TestTestingAPITestVerbTakesWithTestsFrom(t *testing.T) {
	do := testingServer(t, httpd.APIDeps{Testing: litmus.New(litmus.NewStore(t.TempDir()), nil).WithGH(recordedGH{t: t})})
	do("POST", "/test", `{"sessionId":"ao-1","leg":"head","withTestsFrom":"head","args":["go","test"]}`, http.StatusBadRequest, "TESTING_TEST_NOT_ALLOWED")
	do("POST", "/test", `{"sessionId":"ao-1","leg":"base","withTestsFrom":"base","args":["go","test"]}`, http.StatusBadRequest, "TESTING_TEST_NOT_ALLOWED")
}

func TestTestingAPIStartSpawnsWorker(t *testing.T) {
	var spawned []litmus.SpawnRequest
	svc := litmus.New(litmus.NewStore(t.TempDir()), nil).WithSpawner(func(_ context.Context, r litmus.SpawnRequest) (string, error) {
		spawned = append(spawned, r)
		return "ao-7", nil
	})
	do := testingServer(t, httpd.APIDeps{Testing: svc})
	do("POST", "/start", `{"projectId":"p1","prUrl":"aoagents/agent-orchestrator#6394"}`, http.StatusOK, `"sessionId":"ao-7"`, `"name":"litmus: PR #6394"`)
	do("POST", "/start", `{"prUrl":"aoagents/agent-orchestrator#6394","harness":"codex"}`, http.StatusBadRequest, "TESTING_HARNESS_UNSUPPORTED")
	do("POST", "/start", `{"prUrl":"nope"}`, http.StatusBadRequest, "TESTING_PR_URL_INVALID")
	if len(spawned) != 1 || spawned[0].ProjectID != "p1" || spawned[0].Harness != "claude-code" {
		t.Fatalf("spawned = %+v", spawned)
	}
}

func TestTestingAPIStartNoCommentReachesTheRun(t *testing.T) {
	svc := litmus.New(litmus.NewStore(t.TempDir()), nil).WithGH(recordedGH{t: t}).WithSpawner(func(context.Context, litmus.SpawnRequest) (string, error) { return "ao-7", nil })
	do := testingServer(t, httpd.APIDeps{Testing: svc})
	do("POST", "/start", `{"prUrl":"aoagents/agent-orchestrator#6394","noComment":true}`, http.StatusOK, `"sessionId":"ao-7"`)
	do("POST", "/intake", `{"sessionId":"ao-7","prUrl":"aoagents/agent-orchestrator#6394"}`, http.StatusOK, `"comment":"off"`)
	do("POST", "/submit", `{"sessionId":"ao-7","blocked":true,"reason":"x"}`, http.StatusOK, `"commentState":"off"`)
}

func TestTestingAPIStartNoEvidenceReachesTheRun(t *testing.T) {
	svc := litmus.New(litmus.NewStore(t.TempDir()), nil).WithGH(recordedGH{t: t}).WithSpawner(func(context.Context, litmus.SpawnRequest) (string, error) { return "ao-7", nil })
	do := testingServer(t, httpd.APIDeps{Testing: svc})
	do("POST", "/start", `{"prUrl":"aoagents/agent-orchestrator#6394","noEvidence":true}`, http.StatusOK, `"sessionId":"ao-7"`)
	do("POST", "/intake", `{"sessionId":"ao-7","prUrl":"aoagents/agent-orchestrator#6394"}`, http.StatusOK, `"evidence":"off"`, `"comment":"post"`)
	do("POST", "/submit", `{"sessionId":"ao-7","blocked":true,"reason":"x"}`, http.StatusOK, `"evidenceState":"off"`)
}

func TestTestingAPIUnmountedWithoutService(t *testing.T) {
	testingServer(t, httpd.APIDeps{})("GET", "/status?sessionId=ao-1", "", http.StatusNotFound)
}

// recordedGH replays the gh output recorded for PR 6394 under litmus/testdata,
// so the controller test never reaches the network.
type recordedGH struct{ t *testing.T }

func (g recordedGH) Run(_ context.Context, args ...string) ([]byte, error) {
	g.t.Helper()
	var name string
	switch {
	case len(args) >= 2 && args[0] == "pr" && args[1] == "view":
		name = "pr6394.json"
	case len(args) >= 2 && args[0] == "pr" && args[1] == "diff":
		name = "pr6394.diff"
	default:
		return []byte("{}"), nil
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "litmus", "testdata", name))
	if err != nil {
		g.t.Fatalf("read recorded gh output: %v", err)
	}
	return b, nil
}

func TestTestingAPISubmitSkipBase(t *testing.T) {
	store := litmus.NewStore(t.TempDir())
	do := testingServer(t, httpd.APIDeps{Testing: litmus.New(store, nil).WithGH(recordedGH{t: t})})
	do("POST", "/intake", `{"sessionId":"ao-1","prUrl":"https://github.com/aoagents/agent-orchestrator/pull/6394"}`, http.StatusOK)
	do("POST", "/submit", `{"sessionId":"ao-1","skipBase":"  "}`, http.StatusBadRequest, "TESTING_REASON_REQUIRED")
	do("POST", "/submit", `{"sessionId":"ao-1","skipBase":"base build is broken"}`, http.StatusBadRequest, "TESTING_NO_CHECKS") // the field reaches the service
}

func TestTestingAPITestRoute(t *testing.T) {
	store := litmus.NewStore(t.TempDir())
	do := testingServer(t, httpd.APIDeps{Testing: litmus.New(store, nil).WithGH(recordedGH{t: t})})
	do("POST", "/intake", `{"sessionId":"ao-1","prUrl":"https://github.com/aoagents/agent-orchestrator/pull/6394"}`, http.StatusOK)
	do("POST", "/test", `{"sessionId":"ao-1","leg":"head","args":["go","test"]}`, http.StatusConflict, "TESTING_LEG_NOT_PREPARED")
	do("POST", "/test", `{"sessionId":"ao-1","leg":"head","args":["sh","-c","id"]}`, http.StatusBadRequest, "TESTING_TEST_NOT_ALLOWED")
	checkout := t.TempDir()
	for name, body := range map[string]string{"go.mod": "module fixture\n\ngo 1.21\n", "a_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n"} {
		if err := os.WriteFile(filepath.Join(checkout, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run, err := store.Get("run_ao-1")
	if err != nil {
		t.Fatal(err)
	}
	run.State, run.Legs = litmus.RunPrepared, []litmus.Leg{{Name: "head", Worktree: checkout, State: "prepared"}}
	if err := store.Save(run); err != nil {
		t.Fatal(err)
	}
	do("POST", "/test", `{"sessionId":"ao-1","leg":"head","args":["go","test","./...","-count=1"]}`, http.StatusOK, `"exit":0`, `"summary":"1 ok, 0 FAIL, 0 --- FAIL"`, `1-test.txt`)
}

func TestTestingAPIPrune(t *testing.T) {
	dataDir := t.TempDir()
	checkout := filepath.Join(dataDir, "litmus", "repos", "o__r", strings.Repeat("a", 40))
	if err := os.MkdirAll(checkout, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "f"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour) // checkouts used in the last 10 minutes are kept
	if err := os.Chtimes(checkout, old, old); err != nil {
		t.Fatal(err)
	}
	do := testingServer(t, httpd.APIDeps{Testing: litmus.New(litmus.NewStore(dataDir), nil)})
	do("POST", "/prune", `{"dryRun":true}`, http.StatusOK, `"dryRun":true`, "would remove checkout repos/o__r/", "would prune: 1 checkouts, 0 runs")
	if _, err := os.Stat(checkout); err != nil {
		t.Fatalf("dry run deleted the checkout: %v", err)
	}
	do("POST", "/prune", `{"keepRuns":-1}`, http.StatusBadRequest, "TESTING_PRUNE_INVALID")
	do("POST", "/prune", `{}`, http.StatusOK, `"dryRun":false`, "pruned: 1 checkouts, 0 runs")
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("checkout survived: %v", err)
	}
}
