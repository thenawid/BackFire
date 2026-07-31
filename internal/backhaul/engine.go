package backhaul

import (
	"context"
	"fmt"
	"net"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/metrics"
	"github.com/thenawid/backfire/internal/utils"
)

// Engine runs one backhaul tunnel: it wires the TUN device to the carrier,
// encrypting every packet on the way out and authenticating every frame on the
// way in.
type Engine struct {
	cfg    config.BackhaulConfig
	role   config.Role
	log    *utils.Logger
	framer *Framer
	stats  *metrics.Tunnel
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
	e.setLinked(true)
	defer e.setLinked(false)

	// Close the carrier and TUN when the context ends so both pumps unblock.
	go func() {
		<-ctx.Done()
		car.Close()
		tun.Close()
	}()

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

// tunToCarrier reads packets from the interface, seals them and sends them.
func (e *Engine) tunToCarrier(tun *tunDevice, car carrier) error {
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
func (e *Engine) carrierToTun(car carrier, tun *tunDevice) error {
	for {
		frame, err := car.Receive()
		if err != nil {
			return err
		}
		packet, err := e.framer.Open(frame)
		if err != nil {
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
