package backhaul

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/metrics"
	"github.com/thenawid/backfire/internal/utils"
)

const (
	// keepaliveInterval is how often each end sends a tiny authenticated frame so
	// the peer can tell the link is alive even when no traffic is flowing. It is
	// short so a freshly started tunnel reports "connected" within a few seconds.
	keepaliveInterval = 3 * time.Second
	// linkTimeout is how long without hearing anything from the peer before the
	// link is reported down — several missed keepalives are tolerated so a little
	// packet loss does not flap the status.
	linkTimeout = 12 * time.Second
)

// keepalivePayload is a one-byte "packet" whose leading nibble (0) is not a
// valid IP version, so it can never be confused with a real TUN packet and is
// never injected into the interface. It exists only to prove the peer is there.
var keepalivePayload = []byte{0x00}

func isKeepalive(p []byte) bool { return len(p) == 1 && p[0] == 0x00 }

// tunIO is the packet interface the pumps use, satisfied by *tunDevice. Naming
// it lets the pumps and their link-state logic be tested with a fake in place of
// a real kernel TUN device.
type tunIO interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
}

// Engine runs one backhaul tunnel: it wires the TUN device to the carrier,
// encrypting every packet on the way out and authenticating every frame on the
// way in.
type Engine struct {
	cfg    config.BackhaulConfig
	role   config.Role
	log    *utils.Logger
	framer *Framer
	stats  *metrics.Tunnel

	// lastRecv is the Unix-nano time of the most recent authenticated frame from
	// the peer, used to decide whether the link is currently up.
	lastRecv atomic.Int64
}

// New builds a backhaul engine.
func New(cfg config.BackhaulConfig, role config.Role, log *utils.Logger) (*Engine, error) {
	framer, err := NewFramer(cfg.Token)
	if err != nil {
		return nil, err
	}
	return &Engine{cfg: cfg, role: role, log: log.With("backhaul"), framer: framer}, nil
}

// WithMetrics attaches a metrics tunnel so traffic is accounted for.
func (e *Engine) WithMetrics(t *metrics.Tunnel) *Engine {
	e.stats = t
	return e
}

// Run brings up the interface and the carrier and shuttles packets between them
// until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	// Size the TUN MTU so an encrypted frame still fits under a typical path
	// MTU: the configured MTU already leaves headroom, and the framer overhead
	// plus any carrier header comes out of it.
	tunMTU := e.cfg.MTU - e.framer.Overhead() - 28 // 20 IP + 8 ICMP worst case
	if tunMTU < 576 {
		tunMTU = 576
	}
	tun, err := openTUN(e.cfg.IFName, e.cfg.LocalIP, e.cfg.RemoteIP, tunMTU)
	if err != nil {
		return fmt.Errorf("tun: %w", err)
	}
	defer tun.Close()
	e.log.Infof("interface %s up: %s ↔ %s (mtu %d)", tun.Name(), e.cfg.LocalIP, e.cfg.RemoteIP, tunMTU)

	car, err := newCarrier(carrierParams{
		cfg:      e.cfg,
		isServer: e.role == config.RoleServer,
		peer:     net.ParseIP(e.cfg.Peer),
	})
	if err != nil {
		return fmt.Errorf("carrier: %w", err)
	}
	defer car.Close()
	e.log.Infof("carrier %s ready (spoof: %v)", e.cfg.Carrier, e.cfg.Spoof)
	// The link starts DOWN and only comes up once an authenticated frame is heard
	// from the peer — so the panel and bot never show "connected" for a tunnel
	// whose other end was never reachable.
	e.setLinked(false)
	defer e.setLinked(false)

	// Close the carrier and TUN when the context ends so both pumps unblock.
	go func() {
		<-ctx.Done()
		car.Close()
		tun.Close()
	}()
	go e.keepaliveLoop(ctx, car)
	go e.linkWatchdog(ctx)

	errc := make(chan error, 2)
	go func() { errc <- e.tunToCarrier(tun, car) }()
	go func() { errc <- e.carrierToTun(car, tun) }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
}

// keepaliveLoop sends a tiny authenticated frame to the peer on a fixed
// interval, so the far end can tell the link is alive even when no user traffic
// is flowing. On a server that has not yet learned its peer the carrier simply
// drops the send, so this is always safe to call.
func (e *Engine) keepaliveLoop(ctx context.Context, car carrier) {
	e.sendKeepalive(car) // announce ourselves immediately
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.sendKeepalive(car)
		}
	}
}

// sendKeepalive seals and transmits one liveness frame, ignoring the send when
// the peer is not yet known (the carrier drops it) or on any transient error.
func (e *Engine) sendKeepalive(car carrier) {
	frame, err := e.framer.Seal(keepalivePayload)
	if err != nil {
		return
	}
	_ = car.Send(frame)
}

// linkWatchdog drops the link to "down" when nothing has been heard from the
// peer for linkTimeout, and is what turns the status red after the other end
// goes away.
func (e *Engine) linkWatchdog(ctx context.Context) {
	t := time.NewTicker(keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if e.linkStale(time.Now()) {
				e.setLinked(false)
			}
		}
	}
}

// linkStale reports whether too long has passed since the last authenticated
// frame from the peer for the link to still count as up.
func (e *Engine) linkStale(now time.Time) bool {
	last := e.lastRecv.Load()
	return last == 0 || now.Sub(time.Unix(0, last)) > linkTimeout
}

// tunToCarrier reads packets from the interface, seals them and sends them.
func (e *Engine) tunToCarrier(tun tunIO, car carrier) error {
	buf := make([]byte, 65535)
	for {
		n, err := tun.Read(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		frame, err := e.framer.Seal(buf[:n])
		if err != nil {
			e.log.Warnf("seal: %v", err)
			continue
		}
		if err := car.Send(frame); err != nil {
			return err
		}
		if e.stats != nil {
			e.stats.AddTx(int64(n))
		}
	}
}

// carrierToTun receives frames, opens them and injects the packets. A frame that
// fails authentication — a wrong-token peer, or unrelated traffic that happened
// to arrive on a raw socket — is dropped rather than fatal.
func (e *Engine) carrierToTun(car carrier, tun tunIO) error {
	replied := false
	for {
		frame, err := car.Receive()
		if err != nil {
			return err
		}
		packet, err := e.framer.Open(frame)
		if err != nil {
			continue
		}
		// A frame that authenticates is proof the peer is present: mark the link
		// up and remember when we last heard from it.
		e.lastRecv.Store(time.Now().UnixNano())
		e.setLinked(true)
		// On the very first contact, answer with one keepalive so the peer links
		// promptly too — without this the far side would wait for its own timer.
		// Replying only once keeps the two ends from ping-ponging keepalives.
		if !replied {
			replied = true
			e.sendKeepalive(car)
		}
		// Keepalives exist only to prove liveness; they are never real packets, so
		// they are counted as neither traffic nor injected into the interface.
		if isKeepalive(packet) {
			continue
		}
		if _, err := tun.Write(packet); err != nil {
			return err
		}
		if e.stats != nil {
			e.stats.AddRx(int64(len(packet)))
		}
	}
}

func (e *Engine) setLinked(v bool) {
	if e.stats != nil {
		e.stats.SetLinked(v)
	}
}
