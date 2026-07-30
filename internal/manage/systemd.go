// Package manage owns everything about a tunnel's life on the host: writing its
// config, generating and controlling its systemd unit, and listing/removing
// installed tunnels. It shells out to systemctl rather than talking to the
// D-Bus API to keep the dependency surface tiny and the behaviour transparent.
package manage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/app"
)

// unitTemplate is the systemd unit that runs one tunnel in engine mode. Restart
// is unconditional so a crashed or killed tunnel comes back on its own and
// survives a reboot.
const unitTemplate = `[Unit]
Description=backfire tunnel (%[1]s)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%[2]s -c %[3]s
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

// Install writes a tunnel's config and its systemd unit to disk, then enables
// and (re)starts it.
func Install(name string, cfg *config.Config) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(app.ConfigDir, 0o755); err != nil {
		return err
	}
	cfgPath := app.ConfigPath(name)
	if err := config.Save(cfgPath, cfg); err != nil {
		return err
	}
	if err := os.Chmod(cfgPath, 0o600); err != nil {
		return err
	}
	unit := fmt.Sprintf(unitTemplate, name, app.BinPath, cfgPath)
	unitPath := filepath.Join(app.ServiceDir, app.ServiceName(name))
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", app.ServiceName(name)); err != nil {
		return err
	}
	return systemctl("restart", app.ServiceName(name))
}

// Remove stops and disables a tunnel and deletes its config and unit.
func Remove(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	svc := app.ServiceName(name)
	_ = systemctl("stop", svc)
	_ = systemctl("disable", svc)
	_ = os.Remove(filepath.Join(app.ServiceDir, svc))
	_ = os.Remove(app.ConfigPath(name))
	return systemctl("daemon-reload")
}

// Control runs a systemctl verb (start/stop/restart) against a tunnel.
func Control(verb, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return systemctl(verb, app.ServiceName(name))
}

// Status returns the short `systemctl is-active` result for a tunnel.
func Status(name string) string {
	out, _ := exec.Command("systemctl", "is-active", app.ServiceName(name)).Output()
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "unknown"
	}
	return s
}

// List returns the sorted short names of every installed tunnel.
func List() ([]string, error) {
	entries, err := os.ReadDir(app.ConfigDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
	}
	sort.Strings(names)
	return names, nil
}

// LoadConfig loads an installed tunnel's config by name.
func LoadConfig(name string) (*config.Config, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	return config.Load(app.ConfigPath(name))
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// validateName keeps a tunnel name safe to embed in a filename and unit name.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("tunnel name is empty")
	}
	for _, r := range name {
		if !(r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("tunnel name %q has invalid character %q (use letters, digits, - and _)", name, r)
		}
	}
	return nil
}
