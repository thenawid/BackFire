// Package server implements the exposed half of a tunnel — typically the Iran
// VPS. It publishes the forwarded ports to the outside world, waits for the
// client to link in and prove the token, then hands each inbound end-user
// connection to the client.
//
// How a connection is handed over depends on the transport's mode:
//
//   - mux  — one physical link carries every connection as its own multiplexed
//     stream. The server opens a stream and writes the target on it.
//   - pool — the client parks a set of pre-authenticated links; the server takes
//     a ready one and writes the target directly on it, so no dial or handshake
//     happens while an end user is waiting.
package server

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/metrics"
	"github.com/thenawid/backfire/internal/mux"
	"github.com/thenawid/backfire/internal/pool"
	"github.com/thenawid/backfire/internal/protocol"
	"github.com/thenawid/backfire/internal/transport"
	"github.com/thenawid/backfire/internal/utils"
)

// errNoClient is returned when a forwarded connection arrives before any client
// has linked in, so there is nowhere to send it.
var errNoClient = errors.New("no client linked")

// Server is a running exposed-side tunnel.
type Server struct {
	cfg      config.ServerConfig
	log      *utils.Logger
	forwards []config.Forward
	isMux    bool
	version  string
	// stats is where traffic and connection counts are reported; nil when the
	// engine runs without a metrics registry.
	stats *metrics.Tunnel

	mu      sync.Mutex
	current *mux.Session // mux mode: newest authenticated session, or nil
	ready   *pool.Ready  // pool mode: queue of parked links
}

// WithMetrics attaches a metrics tunnel so traffic is accounted for.
func (s *Server) WithMetrics(t *metrics.Tunnel) *Server {
	s.stats = t
	return s
}

// New builds a Server from its config, resolving the forward table up front so
// a bad entry fails fast instead of at first connection.
func New(cfg config.ServerConfig, version string, log *utils.Logger) (*Server, error) {
	forwards, err := cfg.ParsedForwards()
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:      cfg,
		log:      log.With("server"),
		forwards: forwards,
		isMux:    cfg.Transport.IsMux(),
		version:  version,
	}
	if !s.isMux {
		s.ready = pool.NewReady(utils.Seconds(cfg.Pool.IdleTimeout))
	}
	return s, nil
}

// Run publishes the forwarded ports and accepts client links until ctx is
// cancelled.
func (s *Server) Run(ctx context.Context) error {
	tr, err := transport.Get(s.cfg.Transport)
	if err != nil {
		return err
	}
	ln, err := tr.Listen(s.cfg)
	if err != nil {
		return err
	}
	defer ln.Close()
	s.log.Infof("tunnel listening on %s (%s, %s mode)",
		s.cfg.Bind, s.cfg.Transport, s.cfg.Transport.Mode())

	// Publish every forwarded port once; the listeners outlive individual client
	// sessions and simply refuse connections while no client is linked.
	for _, f := range s.forwards {
		if err := s.publish(ctx, f); err != nil {
			return err
		}
	}

	if !s.isMux {
		go s.reapLoop(ctx)
		defer s.ready.Close()
	}

	// Close the tunnel listener when the context ends so Accept unblocks.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				s.log.Warnf("accept tunnel link: %v", err)
				return err
			}
		}
		go s.onLink(conn)
	}
}

// reapLoop periodically retires parked links that have gone stale, so a quiet
// tunnel does not hand out a connection a NAT has already forgotten.
func (s *Server) reapLoop(ctx context.Context) {
	interval := utils.Seconds(s.cfg.Pool.IdleTimeout) / 2
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := s.ready.ReapStale(); n > 0 {
				s.log.Debugf("reaped %d stale pooled link(s)", n)
			}
		}
	}
}

// onLink authenticates an inbound link and installs it according to the mode.
func (s *Server) onLink(conn net.Conn) {
	utils.SetKeepAlive(conn, s.cfg.KeepAlive)
	peerVersion, err := protocol.ServerHandshake(conn, s.cfg.Token, s.version)
	if err != nil {
		s.log.Warnf("reject %s: %v", conn.RemoteAddr(), err)
		conn.Close()
		return
	}
	if s.stats != nil {
		s.stats.SetPeerVersion(peerVersion)
	}
	if s.isMux {
		s.onMuxLink(conn)
		return
	}
	s.onPooledLink(conn)
}

// onMuxLink installs an authenticated link as the current multiplexed session.
func (s *Server) onMuxLink(conn net.Conn) {
	sess, err := mux.Server(conn, s.cfg.Mux)
	if err != nil {
		s.log.Warnf("mux %s: %v", conn.RemoteAddr(), err)
		conn.Close()
		return
	}
	s.setCurrent(sess)
	s.setLinked(true)
	s.log.Infof("client linked from %s", conn.RemoteAddr())

	// The server never expects the client to open streams; draining Accept is
	// simply how we notice the session has closed.
	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			break
		}
		stream.Close()
	}
	s.clearCurrent(sess)
	s.setLinked(false)
	s.log.Infof("client from %s disconnected", conn.RemoteAddr())
}

// onPooledLink parks an authenticated link in the ready queue.
func (s *Server) onPooledLink(conn net.Conn) {
	role, err := pool.ReadRole(conn)
	if err != nil {
		s.log.Warnf("read link role from %s: %v", conn.RemoteAddr(), err)
		conn.Close()
		return
	}
	switch role {
	case pool.RoleData:
		if !s.ready.Put(conn) {
			conn.Close()
			return
		}
		s.setLinked(true)
		s.log.Debugf("parked link from %s (%d ready)", conn.RemoteAddr(), s.ready.Len())
	case pool.RoleControl:
		// Reserved for metrics/health signalling. Hold it open until the peer
		// hangs up, which is how each side notices the tunnel has gone.
		s.log.Infof("client control link from %s", conn.RemoteAddr())
		_, _ = io.Copy(io.Discard, conn)
		conn.Close()
		s.log.Infof("client control link from %s closed", conn.RemoteAddr())
	}
}

// setLinked records peer presence for the panel and the bot.
func (s *Server) setLinked(v bool) {
	if s.stats != nil {
		s.stats.SetLinked(v)
	}
}

func (s *Server) setCurrent(sess *mux.Session) {
	s.mu.Lock()
	old := s.current
	s.current = sess
	s.mu.Unlock()
	if old != nil && old != sess {
		old.Close() // a fresh client supersedes the previous link
	}
}

func (s *Server) clearCurrent(sess *mux.Session) {
	s.mu.Lock()
	if s.current == sess {
		s.current = nil
	}
	s.mu.Unlock()
	sess.Close()
}

func (s *Server) session() *mux.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// publish opens one forwarded listener and pumps every accepted end-user
// connection to the client.
func (s *Server) publish(ctx context.Context, f config.Forward) error {
	ln, err := net.Listen("tcp", f.Listen)
	if err != nil {
		return err
	}
	s.log.Infof("forward %s -> client:%s", f.Listen, f.Target)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go func() {
		for {
			user, err := ln.Accept()
			if err != nil {
				return
			}
			go s.forward(ctx, user, f.Target)
		}
	}()
	return nil
}

// forward carries one end-user connection to the client, which dials target on
// its side.
func (s *Server) forward(ctx context.Context, user net.Conn, target string) {
	link, err := s.linkFor(ctx)
	if err != nil {
		s.log.Warnf("dropping %s: %v", user.RemoteAddr(), err)
		user.Close()
		return
	}
	if err := protocol.WriteTarget(link, target); err != nil {
		s.log.Warnf("write target: %v", err)
		link.Close()
		user.Close()
		return
	}
	if s.stats != nil {
		s.stats.OpenConn()
		defer s.stats.CloseConn()
		utils.PipeMetered(user, link, s.stats)
		return
	}
	utils.Pipe(user, link)
}

// linkFor returns the stream that will carry one forwarded connection: a fresh
// mux stream, or a ready link from the pool.
func (s *Server) linkFor(ctx context.Context) (net.Conn, error) {
	if s.isMux {
		sess := s.session()
		if sess == nil {
			return nil, errNoClient
		}
		return sess.OpenStream()
	}
	// Wait briefly: a link may be in flight while the client refills the pool.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.ready.Get(waitCtx)
}
