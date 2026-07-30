// Package transport abstracts "how do two peers get a raw byte stream between
// them" away from everything above it. Each transport turns the configured
// wire protocol into an ordinary net.Conn on the client side and a net.Listener
// on the server side; the auth handshake and the stream multiplexer are layered
// on top and neither knows nor cares which transport carries them.
package transport

import (
	"context"
	"fmt"
	"net"

	"github.com/thenawid/backfire/config"
)

// Transport is a bidirectional stream provider for one wire protocol.
type Transport interface {
	// Listen opens the server-side listener that accepts inbound client links.
	Listen(cfg config.ServerConfig) (net.Listener, error)
	// Dial opens one client-side link to the server.
	Dial(ctx context.Context, cfg config.ClientConfig) (net.Conn, error)
}

// Get returns the transport implementation for a configured wire protocol.
func Get(t config.Transport) (Transport, error) {
	switch t {
	case config.TCP:
		return tcpTransport{}, nil
	case config.WS:
		return wsTransport{secure: false}, nil
	case config.WSS:
		return wsTransport{secure: true}, nil
	default:
		return nil, fmt.Errorf("unsupported transport %q", t)
	}
}
