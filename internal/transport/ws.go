package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/thenawid/backfire/config"
)

// tunnelPath is the HTTP path the WebSocket handshake targets. A plausible path
// helps the link blend in with ordinary web traffic through a CDN or reverse
// proxy.
const tunnelPath = "/tunnel"

// wsTransport carries the tunnel inside WebSocket binary frames, optionally over
// TLS (secure=true → wss). This survives CDNs and layer-7 proxies that would
// drop a raw TCP tunnel.
type wsTransport struct {
	secure bool
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
	// The token handshake — not the HTTP Origin — is what authenticates a peer,
	// so cross-origin checks would only reject legitimate clients.
	CheckOrigin: func(*http.Request) bool { return true },
}

func (t wsTransport) Listen(cfg config.ServerConfig) (net.Listener, error) {
	raw, err := net.Listen("tcp", cfg.Bind)
	if err != nil {
		return nil, err
	}
	if t.secure {
		tlsCfg, err := serverTLSConfig(cfg)
		if err != nil {
			raw.Close()
			return nil, err
		}
		raw = tls.NewListener(raw, tlsCfg)
	}

	l := &wsListener{
		addr:  raw.Addr(),
		conns: make(chan net.Conn),
		done:  make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(tunnelPath, func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		wc := newWSConn(c)
		select {
		case l.conns <- wc:
		case <-l.done:
			wc.Close()
		}
	})
	l.srv = &http.Server{Handler: mux}
	go l.srv.Serve(raw)
	return l, nil
}

func (t wsTransport) Dial(ctx context.Context, cfg config.ClientConfig) (net.Conn, error) {
	scheme := "ws"
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	if t.secure {
		scheme = "wss"
		sni := cfg.ServerName
		if sni == "" {
			if host, _, err := net.SplitHostPort(cfg.Server); err == nil {
				sni = host
			}
		}
		dialer.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: !cfg.TLSVerify,
			ServerName:         sni,
			MinVersion:         tls.VersionTLS12,
		}
	}
	u := scheme + "://" + cfg.Server + tunnelPath
	c, _, err := dialer.DialContext(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	return newWSConn(c), nil
}

// wsListener adapts an http.Server's upgraded WebSocket connections to the
// net.Listener contract the rest of the stack expects.
type wsListener struct {
	addr      net.Addr
	srv       *http.Server
	conns     chan net.Conn
	done      chan struct{}
	closeOnce sync.Once
}

func (l *wsListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, errors.New("listener closed")
	}
}

func (l *wsListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.done)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = l.srv.Shutdown(ctx)
	})
	return nil
}

func (l *wsListener) Addr() net.Addr { return l.addr }

// wsConn wraps a gorilla WebSocket connection as a stream-oriented net.Conn.
// smux uses exactly one reader goroutine and one writer goroutine on the
// connection, which satisfies gorilla's single-reader / single-writer rule.
type wsConn struct {
	ws     *websocket.Conn
	reader io.Reader
}

func newWSConn(ws *websocket.Conn) *wsConn {
	return &wsConn{ws: ws}
}

func (c *wsConn) Read(p []byte) (int, error) {
	for {
		if c.reader == nil {
			mt, r, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if mt != websocket.BinaryMessage {
				continue
			}
			c.reader = r
		}
		n, err := c.reader.Read(p)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) Close() error                       { return c.ws.Close() }
func (c *wsConn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }

func (c *wsConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}
