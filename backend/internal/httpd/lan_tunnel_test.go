package httpd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

func TestLANTunnelAuthorizesTheUpgradeAndEveryRequestInside(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Proto+" "+r.URL.Path)
	})
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(inner, st, 0, slog.Default(), nil)
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())
	tunnelURL := fmt.Sprintf("ws://127.0.0.1:%d%s", port, lanTunnelPath)

	if _, resp, err := websocket.Dial(context.Background(), tunnelURL, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("upgrade without a password: err=%v resp=%v, want 401", err, resp)
	}

	ws, _, err := websocket.Dial(context.Background(), tunnelURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer secret12"}},
	})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	defer conn.Close()
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	dialed := false
	client := &http.Client{Transport: &http.Transport{
		Protocols: protocols,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if dialed {
				return nil, fmt.Errorf("tunnel already in use")
			}
			dialed = true
			return conn, nil
		},
	}}
	get := func(auth string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, "http://tunnel/anything", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request through the tunnel: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if status, _ := get(""); status != http.StatusUnauthorized {
		t.Fatalf("request without a password inside the tunnel: got %d, want 401", status)
	}
	if status, body := get("Bearer secret12"); status != http.StatusOK || body != "HTTP/2.0 /anything" {
		t.Fatalf("authorized request inside the tunnel: got %d %q", status, body)
	}
}

func TestLANTunnelClosesWhenThePasswordRotates(t *testing.T) {
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), st, 0, slog.Default(), nil)
	port, err := m.Start(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())
	ws, _, err := websocket.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:%d%s", port, lanTunnelPath), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer secret12"}},
	})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	defer conn.Close()

	m.SetPasswordHash(mobilebridge.HashPassword("rotated1"))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("tunnel still open after the password rotated (read: %v)", err)
	}
}
