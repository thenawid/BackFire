package backhaul

import (
	"fmt"
	"net"

	"github.com/thenawid/backfire/config"
)

// A carrier moves opaque frames between the two ends of a backhaul tunnel. The
// engine hands it sealed frames and reads sealed frames back; the carrier knows
// nothing about TUN packets or encryption, only how to put a blob on the wire in
// the shape of its protocol.
type carrier interface {
	// Send transmits one frame toward the peer.
	Send(frame []byte) error
	// Receive blocks for the next frame from the peer. It returns an error when
	// the carrier is closed.
	Receive() ([]byte, error)
	// Close releases the carrier and unblocks a pending Receive.
	Close() error
}

// carrierParams is everything a carrier needs to open, gathered from the config
// and the resolved peer.
type carrierParams struct {
	cfg      config.BackhaulConfig
	isServer bool
	// peer is the resolved remote public address (may be empty for a server that
	// learns it from the first packet).
	peer net.IP
}

// newCarrier builds the carrier for the configured protocol.
func newCarrier(p carrierParams) (carrier, error) {
	switch p.cfg.Carrier {
	case config.CarrierUDP:
		return newUDPCarrier(p)
	case config.CarrierTCP:
		return newTCPCarrier(p)
	case config.CarrierICMP, config.CarrierIPIP, config.CarrierGRE,
		config.CarrierVRRP, config.CarrierBIP:
		return newRawCarrier(p)
	default:
		return nil, fmt.Errorf("unsupported carrier %q", p.cfg.Carrier)
	}
}
