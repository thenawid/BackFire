// Package pool implements the warm connection pool used by the non-multiplexed
// transports (tcp, ws, wss).
//
// The problem it solves: without multiplexing, every forwarded connection needs
// its own transport link, and dialing one plus running the token handshake costs
// at least a round trip — on an intercontinental path, often several hundred
// milliseconds added to the very first byte the end user waits for.
//
// The fix is to move that cost off the critical path. The client keeps a set of
// links pre-dialed and already authenticated, parked and waiting. Each parked
// link announces itself to the server, which keeps them in a ready queue; when a
// connection arrives the server takes a ready link and starts forwarding
// immediately, and the client dials a replacement to refill the pool.
//
// The wire contract is deliberately tiny: after the shared token handshake the
// client sends one role byte saying what the link is for, and for a data link
// the server replies with the target address to dial.
package pool

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Role marks what a pooled link is for. It is the first byte a client sends
// after the token handshake.
type Role byte

const (
	// RoleData is a link parked to carry one forwarded connection.
	RoleData Role = 1
	// RoleControl is the single link a client keeps for out-of-band signalling.
	// Reserved for metrics and health reporting; parked links carry the data.
	RoleControl Role = 2
)

// WriteRole announces a freshly authenticated link's purpose to the server.
func WriteRole(w io.Writer, r Role) error {
	_, err := w.Write([]byte{byte(r)})
	return err
}

// ReadRole reads the role byte a client sent.
func ReadRole(r io.Reader) (Role, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	switch Role(b[0]) {
	case RoleData:
		return RoleData, nil
	case RoleControl:
		return RoleControl, nil
	default:
		return 0, fmt.Errorf("unknown link role %d", b[0])
	}
}

// entry is one parked link plus when it was parked, so the reaper can retire
// links that have sat long enough for a NAT mapping to have gone stale.
type entry struct {
	conn   net.Conn
	parked time.Time
}

// Ready is the server-side queue of authenticated links waiting to carry a
// forwarded connection. Get blocks briefly for a link to arrive rather than
// failing instantly, which smooths over the window while the client refills.
type Ready struct {
	mu     sync.Mutex
	links  []entry
	wait   chan struct{} // closed-and-replaced to wake waiters
	idleTO time.Duration
	closed bool
}

// NewReady builds an empty ready queue. idleTimeout is how long a parked link
// may sit before it is considered stale and dropped.
func NewReady(idleTimeout time.Duration) *Ready {
	return &Ready{
		wait:   make(chan struct{}),
		idleTO: idleTimeout,
	}
}

// Put parks an authenticated link. It returns false when the queue is closed, so
// the caller can hang up rather than leaking the connection.
func (r *Ready) Put(conn net.Conn) bool {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return false
	}
	r.links = append(r.links, entry{conn: conn, parked: time.Now()})
	// Wake everyone waiting; a fresh channel arms the next wait.
	close(r.wait)
	r.wait = make(chan struct{})
	r.mu.Unlock()
	return true
}

// Get takes a ready link, waiting up to the context's deadline for one to be
// parked. Stale links are discarded rather than returned.
func (r *Ready) Get(ctx context.Context) (net.Conn, error) {
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, fmt.Errorf("pool closed")
		}
		for len(r.links) > 0 {
			// Take from the end: the most recently parked link is the least
			// likely to have gone stale.
			e := r.links[len(r.links)-1]
			r.links = r.links[:len(r.links)-1]
			if r.idleTO > 0 && time.Since(e.parked) > r.idleTO {
				e.conn.Close()
				continue
			}
			r.mu.Unlock()
			return e.conn, nil
		}
		wait := r.wait
		r.mu.Unlock()

		select {
		case <-wait:
			// A link was parked (or the queue closed) — loop and re-check.
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Len reports how many links are currently parked.
func (r *Ready) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.links)
}

// Close drops every parked link and unblocks all waiters.
func (r *Ready) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	links := r.links
	r.links = nil
	close(r.wait)
	r.mu.Unlock()

	for _, e := range links {
		e.conn.Close()
	}
}

// ReapStale drops links that have been parked longer than the idle timeout. The
// server runs this periodically so a quiet tunnel does not accumulate links that
// a NAT or stateful firewall has already forgotten.
func (r *Ready) ReapStale() int {
	if r.idleTO <= 0 {
		return 0
	}
	r.mu.Lock()
	kept := r.links[:0]
	var dead []net.Conn
	for _, e := range r.links {
		if time.Since(e.parked) > r.idleTO {
			dead = append(dead, e.conn)
			continue
		}
		kept = append(kept, e)
	}
	r.links = kept
	r.mu.Unlock()

	for _, c := range dead {
		c.Close()
	}
	return len(dead)
}
