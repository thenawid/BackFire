// Package transport abstracts "how do two peers get a raw byte stream between
// them" away from everything above it.
//
// Each Base turns one wire protocol into an ordinary net.Conn on the client
// side and a net.Listener on the server side. The token handshake, the stream
// multiplexer and the connection pool are layered on top and none of them know
// which base carries them — which is why nine configurable transports need only
// five stream providers plus two sharing modes.
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

// Get returns the stream provider backing a configured transport. Callers that
// care about mux-vs-pool consult config.Transport.Mode instead; this only
// resolves how a single raw link is obtained.
func Get(t config.Transport) (Transport, error) {
	switch t.Base() {
	case config.BaseTCP:
		return tcpTransport{}, nil
	case config.BaseStealth:
		return stealthTransport{}, nil
	case config.BaseUDP:
		return kcpTransport{stealth: false}, nil
	case config.BaseKCP:
		return kcpTransport{stealth: true}, nil
	case config.BaseWS:
		return wsTransport{secure: false}, nil
	case config.BaseWSS:
		return wsTransport{secure: true}, nil
	default:
		return nil, fmt.Errorf("unsupported transport %q", t)
	}
}
