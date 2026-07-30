// Package server implements the exposed half of a tunnel — typically the Iran
// VPS. It publishes the forwarded ports to the outside world, waits for the
// client to dial in and prove the token, then hands each inbound end-user
// connection to the client as its own multiplexed stream.
package server

import (
	"context"
	"net"
	"sync"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/mux"
	"github.com/thenawid/backfire/internal/protocol"
	"github.com/thenawid/backfire/internal/transport"
	"github.com/thenawid/backfire/internal/utils"
)

// Server is a running exposed-side tunnel.
type Server struct {
	cfg      config.ServerConfig
	log      *utils.Logger
	forwards []config.Forward

	mu      sync.Mutex
	current *mux.Session // the newest authenticated client session, or nil
}

// New builds a Server from its config, resolving the forward table up front so
// a bad entry fails fast instead of at first connection.
func New(cfg config.ServerConfig, log *utils.Logger) (*Server, error) {
	forwards, err := cfg.ParsedForwards()
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, log: log.With("server"), forwards: forwards}, nil
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
	s.log.Infof("tunnel listening on %s (%s)", s.cfg.Bind, s.cfg.Transport)

	// Publish every forwarded port once; the listeners outlive individual
	// client sessions and simply refuse connections while no client is linked.
	for _, f := range s.forwards {
		if err := s.publish(ctx, f); err != nil {
			return err
		}
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
		go s.onClientLink(conn)
	}
}

// onClientLink authenticates a dialed-in client and, on success, installs its
// multiplexed session as the current one.
func (s *Server) onClientLink(conn net.Conn) {
	utils.SetKeepAlive(conn, s.cfg.KeepAlive)
	if err := protocol.ServerHandshake(conn, s.cfg.Token); err != nil {
		s.log.Warnf("reject %s: %v", conn.RemoteAddr(), err)
		conn.Close()
		return
	}
	sess, err := mux.Server(conn, s.cfg.Mux)
	if err != nil {
		s.log.Warnf("mux %s: %v", conn.RemoteAddr(), err)
		conn.Close()
		return
	}
	s.setCurrent(sess)
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
	s.log.Infof("client from %s disconnected", conn.RemoteAddr())
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
// connection to the current client session.
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
			go s.forward(user, f.Target)
		}
	}()
	return nil
}

// forward carries one end-user connection over a new stream to the client,
// which dials f.Target on its side.
func (s *Server) forward(user net.Conn, target string) {
	sess := s.session()
	if sess == nil {
		s.log.Warnf("dropping %s: no client linked", user.RemoteAddr())
		user.Close()
		return
	}
	stream, err := sess.OpenStream()
	if err != nil {
		s.log.Warnf("open stream: %v", err)
		user.Close()
		return
	}
	if err := protocol.WriteTarget(stream, target); err != nil {
		s.log.Warnf("write target: %v", err)
		stream.Close()
		user.Close()
		return
	}
	utils.Pipe(user, stream)
}
