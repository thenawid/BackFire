// Package cmd builds the default configurations the interactive menu starts
// from, so a freshly created tunnel is fully tuned without the operator editing
// TOML by hand.
package cmd

import (
	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/utils"
)

// DefaultServerConfig returns a ready-to-run server tunnel config with a fresh
// random token.
func DefaultServerConfig() *config.Config {
	return &config.Config{
		Role: config.RoleServer,
		Server: config.ServerConfig{
			Bind:      "0.0.0.0:6060",
			Transport: config.TCP,
			Token:     utils.GenToken(24),
			Forwards:  []string{},
			KeepAlive: 30,
		},
		Log: config.LogConfig{Level: "info"},
	}
}

// DefaultBackhaulConfig returns a ready-to-run layer-3 tunnel config for the
// given role, with a fresh token and the point-to-point tunnel addresses filled
// in for each side.
func DefaultBackhaulConfig(role config.Role) *config.Config {
	local, remote := "10.200.0.1", "10.200.0.2"
	if role == config.RoleClient {
		local, remote = "10.200.0.2", "10.200.0.1"
	}
	return &config.Config{
		Family: config.FamilyBackhaul,
		Role:   role,
		Backhaul: config.BackhaulConfig{
			Carrier:  config.CarrierICMP,
			Port:     2000,
			Token:    utils.GenToken(24),
			LocalIP:  local,
			RemoteIP: remote,
			MTU:      1400,
		},
		Log: config.LogConfig{Level: "info"},
	}
}

// DefaultClientConfig returns a ready-to-run client tunnel config.
func DefaultClientConfig() *config.Config {
	return &config.Config{
		Role: config.RoleClient,
		Client: config.ClientConfig{
			Server:    "203.0.113.10:6060",
			Transport: config.TCP,
			Token:     "",
			KeepAlive: 30,
		},
		Log: config.LogConfig{Level: "info"},
	}
}
