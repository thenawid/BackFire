package transport

import (
	"context"
	"net"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/utils"
)

// tcpTransport is a raw TCP stream: the lowest-overhead option and the baseline
// every other transport is measured against.
type tcpTransport struct{}

func (tcpTransport) Listen(cfg config.ServerConfig) (net.Listener, error) {
	return net.Listen("tcp", cfg.Bind)
}

func (tcpTransport) Dial(ctx context.Context, cfg config.ClientConfig) (net.Conn, error) {
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", cfg.Server)
	if err != nil {
		return nil, err
	}
	utils.SetKeepAlive(conn, cfg.KeepAlive)
	return conn, nil
}
