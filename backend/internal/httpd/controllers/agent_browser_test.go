package controllers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
)

// The agent browser reaches the desktop through the daemon: the browser's
// token is added on the way, AO's credential and the desktop's origin are not
// passed on, the stream socket works, and only the viewer's routes are open.
func TestSessionsAPI_AgentBrowserProxy(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	browser := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.RequestURI()+" auth="+r.Header.Get("Authorization")+" origin="+r.Header.Get("Origin")+" cookie="+r.Header.Get("Cookie"))
		mu.Unlock()
		if r.URL.Path == "/ws/cdp" {
			conn, err := websocket.Accept(w, r, nil) // refuses a foreign Origin, as rbhost does
			if err != nil {
				t.Errorf("accept: %v", err)
				return
			}
			defer func() { _ = conn.CloseNow() }()
			kind, msg, err := conn.Read(r.Context())
			if err == nil {
				err = conn.Write(r.Context(), kind, msg)
			}
			if err != nil {
				t.Errorf("echo: %v", err)
			}
			return
		}
		_, _ = io.WriteString(w, "viewer")
	}))
	t.Cleanup(browser.Close)
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{AgentBrowserURL: browser.URL, AgentBrowserToken: "rb-secret"},
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{Sessions: newFakeSessionService()}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	get := func(path string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer host-password")
		req.Header.Set("Origin", "https://ao-preview-0123.localhost:47663")
		req.Header.Set("Cookie", "ao_conn=host-password")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	if code := get("/api/v1/sessions/ao-1/agent-browser/v/cdp?token=null&sq=70"); code != http.StatusOK {
		t.Fatalf("viewer page status = %d, want 200", code)
	}
	mu.Lock()
	first := seen[0]
	mu.Unlock()
	if first != "/v/cdp?sq=70&token=rb-secret auth= origin= cookie=" {
		t.Fatalf("browser saw %q, want the viewer path with its own token and no AO credential, origin or cookie", first)
	}
	for _, path := range []string{"agent", "debug/reset", "download/a/b", "devtools/page/x"} {
		if code := get("/api/v1/sessions/ao-1/agent-browser/" + path); code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 (not a viewer route)", path, code)
		}
	}
	if code := get("/api/v1/sessions/missing/agent-browser/stats"); code != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", code)
	}

	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/sessions/ao-1/agent-browser/ws/cdp", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer host-password"}, "Origin": {"https://ao-preview-0123.localhost:47663"}},
	})
	if err != nil {
		t.Fatalf("stream socket through the daemon: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("frame")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := conn.Read(context.Background()); err != nil || string(msg) != "frame" {
		t.Fatalf("echo = %q, %v; want frame", msg, err)
	}
}

func TestSessionsAPI_AgentBrowserAbsent(t *testing.T) {
	srv := newSessionTestServer(t, newFakeSessionService())
	res, err := http.Get(srv.URL + "/api/v1/sessions/ao-1/agent-browser/stats")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 on a host with no agent browser", res.StatusCode)
	}
}
