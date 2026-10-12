package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/renderpage"
)

const lintCleanPage = `<!doctype html><main class="ao-page"><header><h1>Turns by day</h1>` +
	`<p class="ao-lede">Most turns land on Tuesday.</p></header></main>`

// --lint and --guide read nothing from the daemon, so they work outside a chat session.
func TestRenderLintAndGuideRunWithoutASession(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	setConfigEnv(t)
	deps := Deps{ProcessAlive: func(int) bool { return true }}

	out, errOut, err := executeCLI(t, deps, "render", "--lint", writePage(t, []byte(lintCleanPage)))
	if err != nil || !strings.Contains(out, "follows the AO page design rules") {
		t.Fatalf("clean page: err=%v stdout=%q stderr=%q", err, out, errOut)
	}
	_, _, err = executeCLI(t, deps, "render", "--lint", writePage(t, []byte(`<div class="card">x</div>`)))
	if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), `class "card" is not an AO class`) {
		t.Fatalf("broken page: err=%v exit=%d", err, ExitCode(err))
	}
	out, _, err = executeCLI(t, deps, "render", "--guide")
	if err != nil || out != renderpage.Guide {
		t.Fatalf("--guide: err=%v stdout=%.80q", err, out)
	}
}

func TestReportRefusesAnHTMLArtifactThatBreaksTheDesignRules(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "worker-1")
	dir := t.TempDir()
	broken := filepath.Join(dir, "report.html")
	clean := filepath.Join(dir, "clean.html")
	for path, body := range map[string]string{broken: `<div style="color:red">x</div>`, clean: lintCleanPage} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name      string
		reference string
		wantSent  bool
	}{
		{"broken page", broken, false},
		{"clean page", clean, true},
		// Only a local HTML file is read; any other reference stays opaque.
		{"missing html file", filepath.Join(dir, "gone.html"), true},
		{"not html", "opaque://report.html.txt", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			var got reportAPIRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/reports" {
					_ = json.NewDecoder(r.Body).Decode(&got)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"id":"rpt_1"}`)
			}))
			defer srv.Close()
			writeRunFileFor(t, cfg, srv)
			_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
				"report", "--done", "--note", "Ready.", "--artifact", tc.reference)
			sent := got.SessionID != ""
			if sent != tc.wantSent || (err == nil) != tc.wantSent {
				t.Fatalf("sent=%v err=%v, want sent=%v", sent, err, tc.wantSent)
			}
			if !tc.wantSent && !strings.Contains(err.Error(), "(style-attribute)") {
				t.Fatalf("err = %v, want the design findings", err)
			}
		})
	}
}
