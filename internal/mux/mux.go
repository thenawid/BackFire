// Package mux layers a stream multiplexer over a single transport connection so
// that every forwarded end-user connection travels as an independent, flow-
// controlled stream on one physical link. It is a thin, opinionated wrapper
// around xtaci/smux that maps backfire's MuxConfig onto smux's tuning knobs.
package mux

import (
	"net"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/xtaci/smux"
)

// Session is the multiplexed session type callers work with.
type Session = smux.Session

// Stream is a single multiplexed stream.
type Stream = smux.Stream

func smuxConfig(m config.MuxConfig) *smux.Config {
	c := smux.DefaultConfig()
	c.Version = m.Version
	c.KeepAliveInterval = time.Duration(m.KeepAlive) * time.Second
	c.KeepAliveTimeout = time.Duration(m.KeepAlive*3) * time.Second
	c.MaxFrameSize = m.MaxFrameSize
	c.MaxReceiveBuffer = m.MaxReceiveBuffer
	c.MaxStreamBuffer = m.MaxStreamBuffer
	return c
}

// Server builds the multiplexer half for the peer that accepted the transport
// connection (the exposed/server side).
func Server(conn net.Conn, m config.MuxConfig) (*Session, error) {
	return smux.Server(conn, smuxConfig(m))
}

// Client builds the multiplexer half for the peer that dialed the transport
// connection (the origin/client side).
func Client(conn net.Conn, m config.MuxConfig) (*Session, error) {
	return smux.Client(conn, smuxConfig(m))
}
