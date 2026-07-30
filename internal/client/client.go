// Package client implements the origin half of a tunnel — typically the server
// abroad. It dials the exposed peer, proves the token, then services every
// stream the peer opens by connecting to the requested local target and
// splicing the two together. A dropped link is retried with exponential
// backoff so the tunnel heals itself without supervision.
package client

import (
	"context"
	"math/rand"
	"net"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/mux"
	"github.com/thenawid/backfire/internal/protocol"
	"github.com/thenawid/backfire/internal/transport"
	"github.com/thenawid/backfire/internal/utils"
)

// Client is a running origin-side tunnel.
type Client struct {
	cfg     config.ClientConfig
	log     *utils.Logger
	allowed map[string]bool // nil means "allow any target"
}

// New builds a Client from its config.
func New(cfg config.ClientConfig, log *utils.Logger) *Client {
	var allowed map[string]bool
	if len(cfg.AllowedTargets) > 0 {
		allowed = make(map[string]bool, len(cfg.AllowedTargets))
		for _, t := range cfg.AllowedTargets {
			allowed[t] = true
		}
	}
	return &Client{cfg: cfg, log: log.With("client"), allowed: allowed}
}

// Run keeps a link to the server alive until ctx is cancelled, reconnecting
// with backoff whenever the link drops.
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Duration(c.cfg.Reconnect.MinBackoff) * time.Second
	maxBackoff := time.Duration(c.cfg.Reconnect.MaxBackoff) * time.Second

	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.serveOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.log.Warnf("link ended: %v — retrying in %s", err, backoff.Round(time.Second))
		}
		// Full jitter: sleep a random amount in [0, backoff] so a fleet of
		// clients reconnecting after a shared outage does not thunder together.
		wait := time.Duration(rand.Int63n(int64(backoff) + 1))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// serveOnce establishes one link and services it until it fails.
func (c *Client) serveOnce(ctx context.Context) error {
	tr, err := transport.Get(c.cfg.Transport)
	if err != nil {
		return err
	}
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, err := tr.Dial(dialCtx, c.cfg)
	cancel()
	if err != nil {
		return err
	}
	if err := protocol.ClientHandshake(conn, c.cfg.Token); err != nil {
		conn.Close()
		return err
	}
	sess, err := mux.Client(conn, c.cfg.Mux)
	if err != nil {
		conn.Close()
		return err
	}
	defer sess.Close()
	c.log.Infof("linked to %s (%s)", c.cfg.Server, c.cfg.Transport)

	// Close the session when the context ends so AcceptStream unblocks.
	go func() {
		<-ctx.Done()
		sess.Close()
	}()

	for {
		stream, err := sess.AcceptStream()
		if err != nil {
			return err
		}
		go c.handleStream(stream)
	}
}

// handleStream reads the target the server chose, dials it locally and splices
// the two ends together.
func (c *Client) handleStream(stream *mux.Stream) {
	target, err := protocol.ReadTarget(stream)
	if err != nil {
		c.log.Warnf("read target: %v", err)
		stream.Close()
		return
	}
	if c.allowed != nil && !c.allowed[target] {
		c.log.Warnf("refusing disallowed target %s", target)
		stream.Close()
		return
	}
	local, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		c.log.Warnf("dial %s: %v", target, err)
		stream.Close()
		return
	}
	utils.Pipe(local, stream)
}
