package app

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/backhaul"
	"github.com/thenawid/backfire/internal/client"
	"github.com/thenawid/backfire/internal/metrics"
	"github.com/thenawid/backfire/internal/server"
	"github.com/thenawid/backfire/internal/utils"
)

// Run starts the engine for a single tunnel config and blocks until ctx is
// cancelled or the tunnel fails fatally. It is the entry point shared by both
// the systemd unit (engine mode) and any in-process caller.
//
// name identifies the tunnel in the panel and the bot; it is the config's base
// filename. While the engine runs it publishes a state snapshot for those
// readers, since they are separate processes and cannot see these counters
// directly.
func Run(ctx context.Context, name string, cfg *config.Config) error {
	log := utils.NewLogger(cfg.Log.Level)

	reg := metrics.NewRegistry()
	done := make(chan struct{})
	defer close(done)
	go reg.Run(done)

	// The Backhaul family is a layer-3 tunnel, dispatched separately from the
	// BackPack transports.
	if cfg.Family == config.FamilyBackhaul {
		return runBackhaul(ctx, name, cfg, reg, done, log)
	}

	switch cfg.Role {
	case config.RoleServer:
		stats := reg.Register(name, string(config.RoleServer),
			string(cfg.Server.Transport), portOf(cfg.Server.Bind), forwardPorts(cfg.Server))
		go stats.PublishLoop(done)

		srv, err := server.New(cfg.Server, log)
		if err != nil {
			return err
		}
		return srv.WithMetrics(stats).Run(ctx)

	case config.RoleClient:
		stats := reg.Register(name, string(config.RoleClient),
			string(cfg.Client.Transport), portOf(cfg.Client.Server), nil)
		go stats.PublishLoop(done)

		return client.New(cfg.Client, log).WithMetrics(stats).Run(ctx)

	default:
		return fmt.Errorf("unknown role %q", cfg.Role)
	}
}

// runBackhaul starts the layer-3 tunnel engine with its own metrics tunnel.
func runBackhaul(ctx context.Context, name string, cfg *config.Config,
	reg *metrics.Registry, done chan struct{}, log *utils.Logger) error {
	stats := reg.Register(name, string(cfg.Role),
		string(cfg.Backhaul.Carrier), cfg.Backhaul.Port, backhaulPorts(cfg.Backhaul))
	go stats.PublishLoop(done)

	eng, err := backhaul.New(cfg.Backhaul, cfg.Role, log)
	if err != nil {
		return err
	}
	return eng.WithMetrics(stats).Run(ctx)
}

// backhaulPorts lists the published ports of a backhaul config, for display.
func backhaulPorts(cfg config.BackhaulConfig) []int {
	forwards, err := cfg.ParsedForwards()
	if err != nil {
		return nil
	}
	out := make([]int, 0, len(forwards))
	for _, f := range forwards {
		if p := portOf(f.Listen); p != 0 {
			out = append(out, p)
		}
	}
	return out
}

// portOf extracts the port from a host:port address, or 0 if it has none.
func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return 0
	}
	return n
}

// forwardPorts lists the published ports of a server config, for display.
func forwardPorts(cfg config.ServerConfig) []int {
	forwards, err := cfg.ParsedForwards()
	if err != nil {
		return nil
	}
	out := make([]int, 0, len(forwards))
	for _, f := range forwards {
		if p := portOf(f.Listen); p != 0 {
			out = append(out, p)
		}
	}
	return out
}
