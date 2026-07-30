package transport

import (
	"context"
	"crypto/sha256"
	"net"
	"time"

	"github.com/thenawid/backfire/config"
	kcp "github.com/xtaci/kcp-go/v5"
	"golang.org/x/crypto/pbkdf2"
)

// The udp and kcp transports both carry the tunnel inside UDP datagrams using
// the KCP protocol, which supplies the reliable, ordered stream that the auth
// handshake and the multiplexer above require — raw UDP cannot.
//
// They differ in what is layered on the datagrams:
//
//   - udp  (stealth=false): KCP with no block cipher and no forward error
//     correction. The lightest way to get a reliable stream over UDP, for paths
//     where TCP is throttled but nothing is inspecting payloads.
//   - kcp  (stealth=true):  KCP with AES-256 keyed from the token plus
//     Reed-Solomon forward error correction, so moderate packet loss is repaired
//     without waiting for a retransmit. The right choice on a lossy or actively
//     degraded path.
type kcpTransport struct {
	// stealth enables AES-256 encryption and FEC (the "kcp" transport).
	stealth bool
}

// kcpSalt is a fixed, public salt: the secret is the token, and both ends must
// derive the same key without exchanging anything.
var kcpSalt = []byte("backfire-kcp-v1")

// blockCipher returns the KCP block cipher for the transport, or nil when
// encryption is disabled.
func (t kcpTransport) blockCipher(token string) (kcp.BlockCrypt, error) {
	if !t.stealth {
		return nil, nil
	}
	key := pbkdf2.Key([]byte(token), kcpSalt, 4096, 32, sha256.New)
	return kcp.NewAESBlockCrypt(key)
}

// shards returns the FEC parameters for the transport. FEC is only meaningful
// for the encrypted/lossy-path variant; plain udp keeps the wire minimal.
func (t kcpTransport) shards(k config.KCPConfig) (data, parity int) {
	if !t.stealth {
		return 0, 0
	}
	if k.DataShards > 0 && k.ParityShards > 0 {
		return k.DataShards, k.ParityShards
	}
	// A 10:3 ratio repairs up to ~23% loss in a block at a 30% bandwidth cost —
	// the usual sweet spot for an intercontinental path.
	return 10, 3
}

func (t kcpTransport) Listen(cfg config.ServerConfig) (net.Listener, error) {
	block, err := t.blockCipher(cfg.Token)
	if err != nil {
		return nil, err
	}
	data, parity := t.shards(cfg.KCP)
	ln, err := kcp.ListenWithOptions(cfg.Bind, block, data, parity)
	if err != nil {
		return nil, err
	}
	if buf := cfg.KCP.SocketBuf; buf > 0 {
		_ = ln.SetReadBuffer(buf)
		_ = ln.SetWriteBuffer(buf)
	}
	return &kcpListener{Listener: ln, tune: cfg.KCP}, nil
}

func (t kcpTransport) Dial(ctx context.Context, cfg config.ClientConfig) (net.Conn, error) {
	block, err := t.blockCipher(cfg.Token)
	if err != nil {
		return nil, err
	}
	data, parity := t.shards(cfg.KCP)

	// kcp.DialWithOptions has no context form; run it in a goroutine so a
	// cancelled context stops us waiting on it.
	type result struct {
		conn *kcp.UDPSession
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := kcp.DialWithOptions(cfg.Server, block, data, parity)
		ch <- result{s, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		tuneSession(r.conn, cfg.KCP)
		return r.conn, nil
	}
}

// kcpListener applies the tuning to each accepted session, since KCP options are
// per-session rather than inherited from the listener.
type kcpListener struct {
	*kcp.Listener
	tune config.KCPConfig
}

func (l *kcpListener) Accept() (net.Conn, error) {
	s, err := l.Listener.AcceptKCP()
	if err != nil {
		return nil, err
	}
	tuneSession(s, l.tune)
	return s, nil
}

// tuneSession applies the configured KCP parameters to one session.
func tuneSession(s *kcp.UDPSession, k config.KCPConfig) {
	s.SetStreamMode(true)
	s.SetMtu(k.MTU)
	s.SetWindowSize(k.SndWnd, k.RcvWnd)
	s.SetNoDelay(k.NoDelay, k.Interval, k.Resend, k.NoCongestion)
	// ACK without delay pairs with a low tick interval to keep latency down on
	// an interactive tunnel.
	s.SetACKNoDelay(true)
	s.SetDeadline(time.Time{})
	if k.SocketBuf > 0 {
		_ = s.SetReadBuffer(k.SocketBuf)
		_ = s.SetWriteBuffer(k.SocketBuf)
	}
}
