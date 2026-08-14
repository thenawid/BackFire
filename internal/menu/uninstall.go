package menu

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/thenawid/backfire/internal/app"
	"github.com/thenawid/backfire/internal/backup"
	"github.com/thenawid/backfire/internal/manage"
	"github.com/thenawid/backfire/internal/metrics"
	"github.com/thenawid/backfire/internal/optimize"
)

// sourceDir is where install.sh puts the source tree when it falls back to a
// build; removed on uninstall if present.
const sourceDir = "/opt/backfire-src"

// uninstallAll removes backfire and everything it installed: every tunnel and
// its unit, the web panel and bot services, the server optimization drop-ins,
// the config and state directories, and finally the binary itself. It is the
// single "remove it all" action, guarded by a clear warning and a confirmation.
func uninstallAll() error {
	title("Uninstall backfire")
	warn("This removes backfire and EVERYTHING it installed on this server:")
	fmt.Println()

	tunnels, _ := manage.List()
	if len(tunnels) > 0 {
		note("• %d tunnel(s) and their systemd units: %v", len(tunnels), tunnels)
	} else {
		note("• any installed tunnels and their systemd units")
	}
	note("• the web panel service (%s)", app.WebUIService)
	note("• the Telegram bot service (%s)", app.BotService)
	note("• the server-optimization files in /etc/sysctl.d and /etc/security/limits.d")
	note("• all configs and tokens in %s", app.ConfigDir)
	note("• the runtime state in %s", metrics.StateDir)
	note("• the backfire binary at %s", app.BinPath)
	fmt.Println()
	warn("Configs and tokens are deleted. This cannot be undone.")
	fmt.Println()

	// Offer a backup first, since the tokens are about to be gone for good.
	if askYesNo("Save a backup of the configs first", true) {
		if path, err := saveBackup(); err != nil {
			fail("backup failed: %v", err)
			if !askYesNo("Continue uninstalling WITHOUT a backup", false) {
				note("Uninstall cancelled.")
				pause()
				return nil
			}
		} else {
			ok("backup written to %s", path)
			note("Keep this file somewhere safe if you might reinstall.")
		}
		fmt.Println()
	}

	if !askYesNo("Remove backfire and everything above now", false) {
		note("Uninstall cancelled — nothing was removed.")
		pause()
		return nil
	}

	title("Removing")

	// 1) Every tunnel: stops, disables and deletes its unit and config.
	for _, n := range tunnels {
		if err := manage.Remove(n); err != nil {
			fail("remove tunnel %s: %v", n, err)
		} else {
			ok("removed tunnel %s", n)
		}
	}

	// 2) The auxiliary services.
	if err := manage.RemoveWebUI(); err != nil {
		fail("remove web panel service: %v", err)
	} else {
		ok("removed web panel service")
	}
	if err := manage.RemoveBot(); err != nil {
		fail("remove bot service: %v", err)
	} else {
		ok("removed bot service")
	}

	// 3) The optimization drop-ins.
	if err := optimize.Revert(); err != nil {
		fail("revert optimization: %v", err)
	} else {
		ok("removed server-optimization files")
	}

	// 4) Config, state, and any source tree left by the installer.
	removePath(app.ConfigDir, "configs")
	removePath(metrics.StateDir, "runtime state")
	if _, err := os.Stat(sourceDir); err == nil {
		removePath(sourceDir, "source tree")
	}

	// 5) The binary last: on Linux the running process keeps its open copy, so
	// deleting the file now is safe and the menu finishes normally.
	if err := os.Remove(app.BinPath); err != nil && !os.IsNotExist(err) {
		fail("remove binary %s: %v", app.BinPath, err)
	} else {
		ok("removed the binary at %s", app.BinPath)
	}

	fmt.Println()
	ok("backfire has been uninstalled.")
	note("This menu is running from the now-deleted binary; it will exit when you")
	note("leave. Nothing backfire installed remains on the server.")
	pause()
	return nil
}

// saveBackup writes a full backup archive next to the operator and returns its
// path.
func saveBackup() (string, error) {
	data, err := backup.Build()
	if err != nil {
		return "", err
	}
	dir, err := os.UserHomeDir()
	if err != nil || dir == "" {
		dir = "/root"
	}
	path := filepath.Join(dir, fmt.Sprintf("backfire-backup-%s.tar.gz", time.Now().Format("2006-01-02-1504")))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// removePath deletes a path recursively, reporting the outcome under a label.
func removePath(path, label string) {
	if err := os.RemoveAll(path); err != nil {
		fail("remove %s (%s): %v", label, path, err)
		return
	}
	ok("removed %s (%s)", label, path)
}
