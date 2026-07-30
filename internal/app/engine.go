package app

import (
	"context"
	"fmt"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/client"
	"github.com/thenawid/backfire/internal/server"
	"github.com/thenawid/backfire/internal/utils"
)

// Run starts the engine for a single tunnel config and blocks until ctx is
// cancelled or the tunnel fails fatally. It is the entry point shared by both
// the systemd unit (engine mode) and any in-process caller.
func Run(ctx context.Context, cfg *config.Config) error {
	log := utils.NewLogger(cfg.Log.Level)
	switch cfg.Role {
	case config.RoleServer:
		srv, err := server.New(cfg.Server, log)
		if err != nil {
			return err
		}
		return srv.Run(ctx)
	case config.RoleClient:
		return client.New(cfg.Client, log).Run(ctx)
	default:
		return fmt.Errorf("unknown role %q", cfg.Role)
	}
}
