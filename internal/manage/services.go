package manage

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/thenawid/backfire/internal/app"
)

// serviceTemplate runs one of the auxiliary daemons — the web panel or the
// Telegram bot. Both take a single flag and are restarted unconditionally, so a
// crash or a reboot brings them back without intervention.
const serviceTemplate = `[Unit]
Description=%[1]s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%[2]s %[3]s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// InstallWebUI writes and starts the panel unit.
func InstallWebUI() error {
	return installService(app.WebUIService, "backfire web panel", "-webui")
}

// InstallBot writes and starts the Telegram bot unit.
func InstallBot() error {
	return installService(app.BotService, "backfire telegram bot", "-bot")
}

// RemoveWebUI stops and deletes the panel unit.
func RemoveWebUI() error { return removeService(app.WebUIService) }

// RemoveBot stops and deletes the bot unit.
func RemoveBot() error { return removeService(app.BotService) }

// ServiceStatus returns `systemctl is-active` for a unit.
func ServiceStatus(unit string) string { return statusOf(unit) }

// ControlService runs a systemctl verb against one of the auxiliary units.
func ControlService(verb, unit string) error { return systemctl(verb, unit) }

func installService(unit, description, flag string) error {
	body := fmt.Sprintf(serviceTemplate, description, app.BinPath, flag)
	path := filepath.Join(app.ServiceDir, unit)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", unit); err != nil {
		return err
	}
	return systemctl("restart", unit)
}

func removeService(unit string) error {
	_ = systemctl("stop", unit)
	_ = systemctl("disable", unit)
	_ = os.Remove(filepath.Join(app.ServiceDir, unit))
	return systemctl("daemon-reload")
}
