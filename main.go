// Command backfire is a high-performance reverse tunnel for the Iran ⇄ abroad
// setup, shipped as a single binary with four modes:
//
//   - Engine mode:  backfire -c /etc/backfire/<name>.toml
//     Runs one tunnel (server or client). This is what the tunnel systemd units
//     execute.
//
//   - Panel mode:   backfire -webui
//     Runs the browser panel. This is what backfire-webui.service executes.
//
//   - Bot mode:     backfire -bot
//     Runs the Telegram bot. This is what backfire-bot.service executes.
//
//   - Menu mode:    backfire        (no arguments)
//     Opens the interactive management CLI to create and control everything.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/app"
	"github.com/thenawid/backfire/internal/menu"
	"github.com/thenawid/backfire/internal/telegram"
	"github.com/thenawid/backfire/internal/utils"
	"github.com/thenawid/backfire/internal/webui"
)

func main() {
	configPath := flag.String("c", "", "path to a tunnel config (TOML) — runs in engine mode")
	webPanel := flag.Bool("webui", false, "run the web panel (used by backfire-webui.service)")
	botMode := flag.Bool("bot", false, "run the Telegram bot (used by backfire-bot.service)")
	showVersion := flag.Bool("v", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("backfire", app.Version)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch {
	case *webPanel:
		fail(runWebUI(ctx))
	case *botMode:
		fail(runBot(ctx))
	case *configPath != "":
		fail(runEngine(ctx, *configPath))
	default:
		fail(menu.Run())
	}
}

func runEngine(ctx context.Context, path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// The tunnel's short name is its config's base filename, which is what the
	// panel and the bot show and what its systemd unit is named after.
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return app.Run(ctx, name, cfg)
}

func runWebUI(ctx context.Context) error {
	s, err := app.LoadWebUI()
	if err != nil {
		return fmt.Errorf("web panel is not configured — set it up with `sudo backfire`: %w", err)
	}
	return webui.New(s, utils.NewLogger("info")).Run(ctx)
}

func runBot(ctx context.Context) error {
	s, err := app.LoadTelegram()
	if err != nil {
		return fmt.Errorf("telegram bot is not configured — set it up with `sudo backfire`: %w", err)
	}
	return telegram.New(s, utils.NewLogger("info")).Run(ctx)
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
