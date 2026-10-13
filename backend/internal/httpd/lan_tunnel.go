package httpd

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// lanTunnelPath carries HTTP/2 inside one WebSocket. Some paths to a remote
// host carry WebSockets unchanged but not HTTP/2 requests: a Cloudflare quick
// tunnel answers 429 past 200 requests in flight and holds back small streamed
// writes, and a relay forwards a WebSocket as bytes. Through the tunnel a
// desktop multiplexes a preview's hundreds of module requests over the one
// WebSocket. The upgrade is authorized by the LAN chain, and so is every
// request inside it, exactly as if it had arrived directly.
const lanTunnelPath = "/api/v1/tunnel"

func (m *LANManager) serveTunnel(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	ws.SetReadLimit(1 << 20) // one HTTP/2 frame batch per message
	conn := &tunnelConn{
		Conn:   websocket.NetConn(context.WithoutCancel(r.Context()), ws, websocket.MessageBinary),
		remote: tunnelAddr(r.RemoteAddr),
		closed: make(chan struct{}),
	}
	// Inner requests end with the upgrade (Stop cancels it) and record their
	// credential on the TCP connection that carries them, which a password
	// rotation closes. They inherit nothing else from the upgrade request.
	base, cancel := context.WithCancel(context.WithValue(context.Background(), lanConnContextKey{}, r.Context().Value(lanConnContextKey{})))
	defer cancel()
	defer context.AfterFunc(r.Context(), cancel)()
	srv := &http.Server{
		Handler:           m.handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetUnencryptedHTTP2(true)
	_ = srv.Serve(&oneConnListener{conn: conn})
}

// tunnelConn reports the TCP peer as its remote address, so lockouts and logs
// see the real client rather than the WebSocket.
type tunnelConn struct {
	net.Conn
	remote    net.Addr
	closeOnce sync.Once
	closed    chan struct{}
}

func (c *tunnelConn) RemoteAddr() net.Addr { return c.remote }

func (c *tunnelConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() { close(c.closed) })
	return err
}

type tunnelAddr string

func (a tunnelAddr) Network() string { return "tcp" }
func (a tunnelAddr) String() string  { return string(a) }

// oneConnListener hands http.Server a single connection, then reports closed
// once that connection is, so Serve returns when the tunnel ends.
type oneConnListener struct {
	conn   *tunnelConn
	handed sync.Once
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var first net.Conn
	l.handed.Do(func() { first = l.conn })
	if first != nil {
		return first, nil
	}
	<-l.conn.closed
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
