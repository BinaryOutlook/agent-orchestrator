package httpd

import (
	"context"
	"github.com/aoagents/agent-orchestrator/backend/internal/browserstream"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
)

type browserLiveSessionStub struct{ calls int }

func (s *browserLiveSessionStub) Get(context.Context, domain.SessionID) (domain.Session, error) {
	s.calls++
	return domain.Session{}, nil
}

func TestBrowserLiveIsLANOnly(t *testing.T) {
	sessions := &browserLiveSessionStub{}
	hub := NewBrowserLiveHub(nil, sessions, func() bool { return true }, nil)
	r := chi.NewRouter()
	mountBrowserLive(r, hub)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s1/browser/live", nil)
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("loopback status = %d, want 404", res.Code)
	}
	if sessions.calls != 0 {
		t.Fatalf("session lookup calls = %d, want 0", sessions.calls)
	}
}

func TestBrowserLiveRequiresDesktopOptIn(t *testing.T) {
	sessions := &browserLiveSessionStub{}
	hub := NewBrowserLiveHub(nil, sessions, func() bool { return false }, nil)
	r := chi.NewRouter()
	mountBrowserLive(r, hub)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s1/browser/live", nil)
	req = req.WithContext(requestscope.WithLAN(req.Context()))
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("disabled status = %d, want 403", res.Code)
	}
	if sessions.calls != 0 {
		t.Fatalf("session lookup calls = %d, want 0", sessions.calls)
	}
}

func TestBrowserLiveRechecksDesktopOptInAfterReserve(t *testing.T) {
	sessions := &browserLiveSessionStub{}
	enabledCalls := 0
	hub := NewBrowserLiveHub(nil, sessions, func() bool {
		enabledCalls++
		return enabledCalls == 1
	}, nil)
	r := chi.NewRouter()
	mountBrowserLive(r, hub)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s1/browser/live", nil)
	req = req.WithContext(requestscope.WithLAN(req.Context()))
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("disabled-after-reserve status = %d, want 403", res.Code)
	}
	if enabledCalls != 2 {
		t.Fatalf("enabled calls = %d, want 2", enabledCalls)
	}
	if sessions.calls != 1 {
		t.Fatalf("session lookup calls = %d, want 1", sessions.calls)
	}
	_, release, ok := hub.reserve("s1")
	if !ok {
		t.Fatal("lease was not released after sharing was disabled")
	}
	release()
}

func TestBrowserLiveLeaseIsExclusiveAndRevocable(t *testing.T) {
	hub := NewBrowserLiveHub(nil, nil, nil, nil)
	ctx, release, ok := hub.reserve("s1")
	if !ok {
		t.Fatal("first lease was rejected")
	}
	if _, _, ok := hub.reserve("s1"); ok {
		t.Fatal("second lease was accepted")
	}
	hub.CloseAll()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("CloseAll did not revoke lease")
	}
	release()
	_, releaseAgain, ok := hub.reserve("s1")
	if !ok {
		t.Fatal("lease was not reusable after release")
	}
	releaseAgain()
}

func TestBrowserLivePreviewTargetMatchesWhatTheDesktopWouldOpen(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "index.html"), []byte("<h1>hi</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	hub := NewBrowserLiveHub(nil, nil, nil, nil)
	explicit := domain.Session{ID: "s1", Metadata: domain.SessionMetadata{PreviewURL: " http://localhost:5173/ ", WorkspacePath: workspace}}
	if got := hub.previewTarget(explicit); got != "http://localhost:5173/" {
		t.Fatalf("explicit preview target = %q", got)
	}

	// A workspace entrypoint that was never opened with `ao preview` is what the
	// phone's App preview tab shows, so the streamed browser must open it too.
	discovered := domain.Session{ID: "s1", Metadata: domain.SessionMetadata{WorkspacePath: workspace}}
	if got := hub.previewTarget(discovered); got != "" {
		t.Fatalf("preview target without a loopback origin = %q, want empty", got)
	}
	hub.SetLoopbackBaseURL("http://127.0.0.1:4317")
	got := hub.previewTarget(discovered)
	if !strings.HasPrefix(got, "http://") || !strings.Contains(got, ":4317/") || !strings.HasSuffix(got, "index.html") {
		t.Fatalf("discovered preview target = %q", got)
	}

	empty := domain.Session{ID: "s2", Metadata: domain.SessionMetadata{WorkspacePath: t.TempDir()}}
	if got := hub.previewTarget(empty); got != "" {
		t.Fatalf("preview target for an empty workspace = %q, want empty", got)
	}
}

// A phone can start watching before the agent has produced a page. Keep the
// stream open and verify the newly generated entry is offered without any
// desktop panel being mounted or a mobile reconnect.
type livePreviewSession struct {
	workspace string
	lookedUp  chan struct{}
	once      sync.Once
}

func (s *livePreviewSession) Get(context.Context, domain.SessionID) (domain.Session, error) {
	s.once.Do(func() { close(s.lookedUp) })
	return domain.Session{ID: "s1", Metadata: domain.SessionMetadata{WorkspacePath: s.workspace}}, nil
}

type livePreviewBroker struct{ offers chan string }

func (*livePreviewBroker) Subscribe(context.Context, string) (*browserstream.Subscription, error) {
	return nil, nil
}
func (b *livePreviewBroker) Send(ctx context.Context, _ string, kind string, payload any) error {
	if kind == "preview" {
		select {
		case b.offers <- payload.(map[string]string)["url"]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func TestBrowserLiveOffersPreviewGeneratedAfterConnection(t *testing.T) {
	workspace := t.TempDir()
	sessions := &livePreviewSession{workspace: workspace, lookedUp: make(chan struct{})}
	broker := &livePreviewBroker{offers: make(chan string, 4)}
	hub := NewBrowserLiveHub(broker, sessions, nil, nil)
	hub.SetLoopbackBaseURL("http://127.0.0.1:4317")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); hub.watchPreview(ctx, "s1") }()
	<-sessions.lookedUp
	if err := os.WriteFile(filepath.Join(workspace, "index.html"), []byte("<h1>new page</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case target := <-broker.offers:
			if !strings.HasSuffix(target, ":4317/index.html") {
				t.Fatalf("offered target = %q", target)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("generated preview was not offered or retried")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("preview polling survived stream cancellation")
	}
}
