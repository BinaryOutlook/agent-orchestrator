package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testingCapture struct {
	method, path string
	body         map[string]any
}

// testingDaemon answers every request with status and body, records the last
// one, and points the CLI's run file at itself.
func testingDaemon(t *testing.T, status int, respBody string) *testingCapture {
	t.Helper()
	t.Setenv("AO_SESSION_ID", "w1")
	cfg := setConfigEnv(t)
	got := &testingCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*got = testingCapture{method: r.Method, path: r.URL.RequestURI()}
		_ = json.Unmarshal(raw, &got.body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	return got
}

func runTesting(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, append([]string{"testing"}, args...)...)
	return out, err
}

func TestTestingVerbsSendOneRequestEach(t *testing.T) {
	verdict := filepath.Join(t.TempDir(), "verdict.txt")
	if err := os.WriteFile(verdict, []byte("head only"), 0o600); err != nil {
		t.Fatal(err)
	}
	recipe, _ := filepath.Abs("recipe.yaml")
	type M = map[string]any
	type row struct {
		args, path string
		body       M // sessionId is added to every expected body
	}
	const pr = "https://github.com/aoagents/agent-orchestrator/pull/6394"
	tests := []row{
		{"intake " + pr, "POST /testing/intake", M{"prUrl": pr}},
		{"prepare", "POST /testing/prepare", M{}},
		{"launch head", "POST /testing/launch", M{"leg": "head"}},
		{"launch base", "POST /testing/launch", M{"leg": "base"}},
		{"status", "GET /testing/status?sessionId=w1", nil},
		{"check add menu --expect shown", "POST /testing/checks", M{"name": "menu", "expect": "shown"}},
		{"check add menu --expect shown --leg base", "POST /testing/checks", M{"name": "menu", "expect": "shown", "leg": "base"}},
		{"check start c1", "POST /testing/checks/c1/start", M{}},
		{"compare c1", "POST /testing/checks/c1/compare", M{}},
		{"check end c1 --result fail --note clipped", "POST /testing/checks/c1/end", M{"result": "fail", "note": "clipped"}},
		{"submit", "POST /testing/submit", M{}},
		{"submit --verdict-file " + verdict, "POST /testing/submit", M{"summary": "head only"}},
		{"submit --blocked --reason prepare-refused", "POST /testing/submit", M{"blocked": true, "reason": "prepare-refused"}},
		{"submit --skip-base --reason time-ran-out", "POST /testing/submit", M{"skipBase": "time-ran-out"}},
		{"submit --skip-base --reason time-ran-out --verdict-file " + verdict, "POST /testing/submit", M{"summary": "head only", "skipBase": "time-ran-out"}},
		{"cleanup", "POST /testing/cleanup", M{}},
		{"recipe validate recipe.yaml", "POST /testing/recipe/validate", M{"path": recipe}},
	}
	// Driving verbs all post {action: first word, args: the rest} to /testing/drive.
	for _, d := range []string{"snapshot", "click e12", "fill e3 hello", "type e3 hi", "press Enter", "wait 500", "wait --text Saved", "screenshot", "screenshot a.png", "console", "errors", "network", "record start run.mp4", "record stop"} {
		w := strings.Fields(d)
		tests = append(tests, row{d, "POST /testing/drive", M{"action": w[0], "args": w[1:]}})
	}
	for _, tc := range tests {
		t.Run(tc.args, func(t *testing.T) {
			got := testingDaemon(t, http.StatusOK, `{}`)
			if _, err := runTesting(t, strings.Fields(tc.args)...); err != nil {
				t.Fatal(err)
			}
			method, path, _ := strings.Cut(tc.path, " ")
			if got.method != method || got.path != "/api/v1"+path {
				t.Fatalf("request = %s %s, want %s", got.method, got.path, tc.path)
			}
			if tc.body != nil {
				tc.body["sessionId"] = "w1"
				if fmt.Sprint(got.body) != fmt.Sprint(tc.body) { // Sprint sorts map keys and prints []string like []any
					t.Fatalf("body = %v, want %v", got.body, tc.body)
				}
			}
		})
	}
}

func TestTestingUsageErrors(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "w1")
	for _, args := range []string{
		"intake", "launch", "check add x", "check add --expect y", "check end c1", "check end c1 --result maybe",
		"check start", "check add x --expect y --leg main", "compare", "start", "start --pr x --harness codex", "submit --blocked", "submit --reason x", "submit --skip-base", "submit --skip-base --reason", "submit --skip-base --blocked --reason x", "submit --blocked --reason x --verdict-file f", "click", "fill e1", "type e1", "press", "wait", "wait 5 --text x", "record start", "recipe validate", "status extra",
	} {
		t.Run(args, func(t *testing.T) {
			if _, err := runTesting(t, strings.Fields(args)...); ExitCode(err) != 2 {
				t.Fatalf("err=%v exit=%d", err, ExitCode(err))
			}
		})
	}
	t.Setenv("AO_SESSION_ID", "")
	if _, err := runTesting(t, "status"); ExitCode(err) != 2 || !strings.Contains(err.Error(), "AO_SESSION_ID") {
		t.Fatalf("err=%v", err)
	}
}

func TestTestingOutputAndDaemonErrors(t *testing.T) {
	testingDaemon(t, http.StatusOK, `{"run":{"id":"run_1","state":"intake","repo":"o/r","prNumber":7},"checks":[{"id":"c1","name":"menu","state":"pass","receipts":[{"seq":1}]}]}`)
	out, err := runTesting(t, "status")
	if err != nil || !strings.Contains(out, "run run_1 intake o/r#7") || !strings.Contains(out, "c1 pass menu (1 receipts)") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	testingDaemon(t, http.StatusOK, `{"run":{"id":"run_1","state":"intake","repo":"o/r","prNumber":7,"error":"blocked at prepare: no registered AO project matches o/r"},"checks":[]}`)
	if out, err = runTesting(t, "status"); err != nil || !strings.Contains(out, "\nblocked at prepare: no registered AO project matches o/r") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	testingDaemon(t, http.StatusOK, `{"run":{"id":"run_1","state":"intake","repo":"o/r","prNumber":7},"checks":[]}`)
	var parsed struct{ Run struct{ ID string } }
	if out, err = runTesting(t, "status", "--json"); err != nil || json.Unmarshal([]byte(out), &parsed) != nil || parsed.Run.ID != "run_1" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	testingDaemon(t, http.StatusNotImplemented, `{"message":"not implemented in T1: prepare","code":"TESTING_NOT_IMPLEMENTED"}`)
	if _, err = runTesting(t, "prepare"); ExitCode(err) != 1 || !strings.Contains(err.Error(), "not implemented in T1: prepare (TESTING_NOT_IMPLEMENTED)") {
		t.Fatalf("err=%v", err)
	}
}

func TestTestingStartSpawnsThroughTheDaemon(t *testing.T) {
	const pr = "https://github.com/aoagents/agent-orchestrator/pull/6394"
	// One canned answer serves the project lookup and the start call.
	reply := func(agent string) string {
		return `{"project":{"id":"p1","config":{"worker":{"agent":"` + agent + `"}}},"sessionId":"ao-7","name":"litmus: PR #6394"}`
	}

	got := testingDaemon(t, http.StatusOK, reply("claude-code"))
	out, err := runTesting(t, "start", "--pr", pr, "--project", "p1")
	if err != nil || !strings.Contains(out, `spawned session ao-7 "litmus: PR #6394"`) {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if got.method != "POST" || got.path != "/api/v1/testing/start" ||
		fmt.Sprint(got.body) != fmt.Sprint(map[string]any{"projectId": "p1", "harness": "claude-code", "prUrl": pr}) {
		t.Fatalf("request = %s %s %v", got.method, got.path, got.body)
	}

	got = testingDaemon(t, http.StatusOK, reply("claude-code"))
	if _, err = runTesting(t, "start", "--pr", pr, "--project", "p1", "--no-comment"); err != nil || got.body["noComment"] != true {
		t.Fatalf("--no-comment: err=%v body=%v", err, got.body)
	}

	got = testingDaemon(t, http.StatusOK, reply("claude-code"))
	if _, err = runTesting(t, "start", "--pr", pr, "--project", "p1", "--no-evidence"); err != nil || got.body["noEvidence"] != true || got.body["noComment"] != nil {
		t.Fatalf("--no-evidence: err=%v body=%v", err, got.body)
	}

	got = testingDaemon(t, http.StatusOK, reply("codex")) // the project's worker is not claude-code
	for _, args := range [][]string{{"start", "--pr", pr, "--project", "p1"}, {"start", "--pr", pr, "--project", "p1", "--harness", "codex"}} {
		if _, err := runTesting(t, args...); ExitCode(err) != 2 || !strings.Contains(err.Error(), "claude-code only") || got.path == "/api/v1/testing/start" {
			t.Fatalf("%v: err=%v last request %s", args, err, got.path)
		}
	}
	if _, err := runTesting(t, "start", "--pr", pr, "--project", "p1", "--harness", "claude-code"); err != nil || got.path != "/api/v1/testing/start" {
		t.Fatalf("explicit claude-code over a codex project: err=%v path %s", err, got.path)
	}
}

func TestTestingSubmitPrintsWhatBecameOfTheComment(t *testing.T) {
	for reply, want := range map[string]string{
		`{"overall":"pass","commentState":"posted","commentUrl":"https://github.com/o/r/pull/7#issuecomment-9"}`: "verdict: pass\ncomment posted: https://github.com/o/r/pull/7#issuecomment-9",
		`{"overall":"fail","commentState":"failed:HTTP 403: Resource not accessible"}`:                           "verdict: fail\ncomment failed:HTTP 403: Resource not accessible",
		`{"overall":"blocked","commentState":"off"}`:                                                             "verdict: blocked\ncomment off",
		`{"overall":"pass"}`: "verdict: pass",
	} {
		testingDaemon(t, http.StatusOK, reply)
		if out, err := runTesting(t, "submit"); err != nil || strings.TrimSpace(out) != want {
			t.Errorf("%s: out=%q err=%v", reply, out, err)
		}
	}
}

func TestTestingPruneSendsFlagsAndPrintsLines(t *testing.T) {
	got := testingDaemon(t, http.StatusOK, `{"dryRun":true,"lines":["would remove checkout repos/o__r/abc (1.0 KB)","would prune: 1 checkouts, 0 runs, 0.0 GB freed"]}`)
	out, err := runTesting(t, "prune", "--keep-runs", "5", "--keep-checkouts", "0", "--dry-run")
	if err != nil || got.path != "/api/v1/testing/prune" || fmt.Sprint(got.body) != "map[dryRun:true keepCheckouts:0 keepRuns:5]" {
		t.Fatalf("request = %s %v err=%v", got.path, got.body, err)
	}
	if !strings.Contains(out, "would remove checkout repos/o__r/abc") || !strings.HasSuffix(strings.TrimSpace(out), "0.0 GB freed") {
		t.Fatalf("out = %q", out)
	}
	if _, err := runTesting(t, "prune"); err != nil || fmt.Sprint(got.body) != "map[]" {
		t.Fatalf("defaults must be left to the daemon: body=%v err=%v", got.body, err)
	}
}
