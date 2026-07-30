// Package client implements the origin half of a tunnel — typically the server
// abroad. It links to the exposed peer, proves the token, then services whatever
// the peer sends it by dialing the requested local target and splicing the two
// together. A dropped link is retried with exponential backoff, so the tunnel
// heals itself without supervision.
//
// Two shapes, chosen by the transport's mode:
//
//   - mux  — one link carries every forwarded connection as a multiplexed
//     stream. The client accepts streams and serves each one.
//   - pool — the client keeps a set of links pre-dialed and already past the
//     handshake, parked and waiting. The server takes a ready link when it needs
//     one, and the client immediately dials a replacement, so an end user never
//     waits for a dial or a handshake.
package client

import (
	"context"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/mux"
	"github.com/thenawid/backfire/internal/pool"
	"github.com/thenawid/backfire/internal/protocol"
	"github.com/thenawid/backfire/internal/transport"
	"github.com/thenawid/backfire/internal/utils"
)

// Client is a running origin-side tunnel.
type Client struct {
	cfg     config.ClientConfig
	log     *utils.Logger
	allowed map[string]bool // nil means "allow any target"
	isMux   bool
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
	return &Client{
		cfg:     cfg,
		log:     log.With("client"),
		allowed: allowed,
		isMux:   cfg.Transport.IsMux(),
	}
}

// Run keeps the tunnel alive until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	if c.isMux {
		return c.runMux(ctx)
	}
	return c.runPool(ctx)
}

// runMux keeps one multiplexed link alive, reconnecting with backoff.
func (c *Client) runMux(ctx context.Context) error {
	backoff := c.newBackoff()
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.serveMuxOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			c.log.Warnf("link ended: %v — retrying in ~%s", err, backoff.peek())
		}
		if !backoff.sleep(ctx) {
			return nil
		}
	}
}

// serveMuxOnce establishes one multiplexed link and services it until it fails.
func (c *Client) serveMuxOnce(ctx context.Context) error {
	conn, err := c.dialAuthenticated(ctx)
	if err != nil {
		return err
	}
	sess, err := mux.Client(conn, c.cfg.Mux)
	if err != nil {
		conn.Close()
		return err
	}
	defer sess.Close()
	c.log.Infof("linked to %s (%s, mux mode)", c.cfg.Server, c.cfg.Transport)

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
		go c.serveLink(stream)
	}
}

// runPool keeps the warm pool topped up: Size workers each park a link, serve
// the one connection it is taken for, then park a fresh one.
func (c *Client) runPool(ctx context.Context) error {
	size := c.cfg.Pool.Size
	c.log.Infof("maintaining a pool of %d warm link(s) to %s (%s)",
		size, c.cfg.Server, c.cfg.Transport)

	var wg sync.WaitGroup
	for i := 0; i < size; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			c.poolWorker(ctx, worker)
		}(i)
	}
	wg.Wait()
	return nil
}

// poolWorker parks one link at a time for the life of the tunnel. Each cycle:
// dial, authenticate, announce as a data link, then block until the server hands
// it a target — at which point it serves that one connection and starts over.
func (c *Client) poolWorker(ctx context.Context, worker int) {
	backoff := c.newBackoff()
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := c.dialAuthenticated(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Only the first worker logs, or a pool of 32 would log 32 times for
			// one outage.
			if worker == 0 {
				c.log.Warnf("pool dial failed: %v — retrying in ~%s", err, backoff.peek())
			}
			if !backoff.sleep(ctx) {
				return
			}
			continue
		}
		backoff.reset()

		if err := pool.WriteRole(conn, pool.RoleData); err != nil {
			conn.Close()
			continue
		}
		// Parked. This blocks until the server picks this link and writes a
		// target on it, or the link dies.
		c.serveLink(conn)
	}
}

// serveLink reads the target the server chose for this stream/link, dials it
// locally and splices the two ends together.
func (c *Client) serveLink(link net.Conn) {
	target, err := protocol.ReadTarget(link)
	if err != nil {
		// An idle parked link closed by the server or reaped for staleness ends
		// up here; it is routine, not an error worth shouting about.
		c.log.Debugf("link ended before use: %v", err)
		link.Close()
		return
	}
	if c.allowed != nil && !c.allowed[target] {
		c.log.Warnf("refusing disallowed target %s", target)
		link.Close()
		return
	}
	local, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		c.log.Warnf("dial %s: %v", target, err)
		link.Close()
		return
	}
	utils.Pipe(local, link)
}

// dialAuthenticated opens one transport link and completes the token handshake.
func (c *Client) dialAuthenticated(ctx context.Context) (net.Conn, error) {
	tr, err := transport.Get(c.cfg.Transport)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := tr.Dial(dialCtx, c.cfg)
	if err != nil {
		return nil, err
	}
	if err := protocol.ClientHandshake(conn, c.cfg.Token); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// backoff is an exponential backoff with full jitter, so a fleet of clients
// reconnecting after a shared outage does not thunder together.
type backoff struct {
	cur, min, max time.Duration
}

func (c *Client) newBackoff() *backoff {
	min := utils.Seconds(c.cfg.Reconnect.MinBackoff)
	return &backoff{
		cur: min,
		min: min,
		max: utils.Seconds(c.cfg.Reconnect.MaxBackoff),
	}
}

func (b *backoff) peek() time.Duration { return b.cur.Round(time.Second) }

func (b *backoff) reset() { b.cur = b.min }

// sleep waits a random duration in [0, cur], then doubles cur. It returns false
// if the context ended while waiting.
func (b *backoff) sleep(ctx context.Context) bool {
	wait := time.Duration(rand.Int63n(int64(b.cur) + 1))
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	}
	b.cur *= 2
	if b.cur > b.max {
		b.cur = b.max
	}
	return true
}
