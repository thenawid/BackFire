// Command backfire is a high-performance reverse tunnel for the Iran ⇄ abroad
// setup, shipped as a single binary with two modes:
//
//   - Engine mode:  backfire -c /etc/backfire/<name>.toml
//     Runs one tunnel (server or client). This is what the systemd units
//     execute.
//
//   - Menu mode:    backfire        (no arguments)
//     Opens the interactive management CLI to create and control tunnels.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/app"
	"github.com/thenawid/backfire/internal/menu"
)

func main() {
	configPath := flag.String("c", "", "path to a tunnel config (TOML) — runs in engine mode")
	showVersion := flag.Bool("v", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("backfire", app.Version)
		return
	}

	// No config → interactive menu.
	if *configPath == "" {
		if err := menu.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "engine:", err)
		os.Exit(1)
	}
}
