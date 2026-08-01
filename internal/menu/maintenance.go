package menu

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/thenawid/backfire/internal/app"
	"github.com/thenawid/backfire/internal/manage"
	"github.com/thenawid/backfire/internal/metrics"
	"github.com/thenawid/backfire/internal/optimize"
	"github.com/thenawid/backfire/internal/updater"
)

// optimizeServer applies the network tuning and offers a reboot.
func optimizeServer() error {
	title("Optimize server")
	note("Applies BBR, large socket buffers, aggressive TCP tuning, IP forwarding")
	note("and raised file-descriptor limits — tuned for maximum tunnel throughput.")
	warn("This changes system-wide network settings. It is safe to revert by")
	warn("deleting /etc/sysctl.d/99-backfire.conf and /etc/security/limits.d/99-backfire.conf.")
	fmt.Println()
	if !askYesNo("Apply the optimization now", true) {
		note("Nothing was changed.")
		pause()
		return nil
	}

	rep, err := optimize.Apply()
	if err != nil {
		return err
	}

	title("Optimization applied")
	for _, a := range rep.Applied {
		ok("%s", a)
	}
	for _, w := range rep.Warnings {
		warn("%s", w)
	}
	fmt.Println()

	if rep.RebootRecommended {
		warn("A reboot is recommended so every change (the raised limits especially)")
		warn("takes full effect.")
		if askYesNo("Reboot now", false) {
			note("Rebooting…")
			if err := exec.Command("reboot").Run(); err != nil {
				fail("could not reboot: %v", err)
				note("Reboot manually with: sudo reboot")
			}
			return nil
		}
		fail("Not rebooting now — REMEMBER to reboot later so the changes fully apply:")
		note("   sudo reboot")
	}
	pause()
	return nil
}

// updateBackfire checks for a newer release, installs it without dropping any
// tunnel, and warns about peers that are far behind.
func updateBackfire() error {
	title("Update backfire")
	field("current version", cyan+app.Version+reset)
	note("Checking GitHub for the latest release…")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	chk, err := updater.Check(ctx)
	if err != nil {
		return fmt.Errorf("could not check for updates: %w", err)
	}
	field("latest version", cyan+chk.Latest.Raw+reset)

	// Warn before updating if a linked peer is far behind, so both ends get
	// updated together rather than drifting apart.
	warnStalePeers(chk.Latest)

	if !chk.Available {
		fmt.Println()
		ok("Already on the latest version.")
		pause()
		return nil
	}

	fmt.Println()
	note("Updating replaces the binary in place. Running tunnels keep their open")
	note("copy and are NOT interrupted — they pick up the new version only when you")
	note("restart them (or on the next reboot).")
	if !askYesNo(fmt.Sprintf("Update to %s now", chk.Latest.Raw), true) {
		note("Update cancelled.")
		pause()
		return nil
	}

	res, err := updater.Update(ctx)
	if err != nil {
		return err
	}
	if res.UpToDate {
		ok("Already up to date.")
		pause()
		return nil
	}

	title("Updated")
	ok("Installed %s (was %s).", res.Latest.Raw, res.Current.Raw)
	note("Running tunnels were not interrupted.")
	fmt.Println()
	if askYesNo("Restart the panel and bot services now to run the new version", true) {
		restartAux()
	}
	if askYesNo("Restart the tunnels now too (brief interruption) to run the new version", false) {
		restartAllTunnels()
	} else {
		note("Tunnels keep running the old version until you restart them or reboot.")
	}
	pause()
	return nil
}

// warnStalePeers prints a mutual-update warning for any linked tunnel whose peer
// is far behind the given (latest) version.
func warnStalePeers(latest updater.Version) {
	for _, s := range metrics.ReadAll() {
		if !s.Linked || s.PeerVersion == "" {
			continue
		}
		peer := updater.Parse(s.PeerVersion)
		if updater.MuchOlder(peer, latest) {
			fmt.Println()
			warn("Peer of tunnel '%s' is on %s, far behind %s.", s.Name,
				displayVersion(s.PeerVersion), latest.Raw)
			warn("Update the OTHER server too, or the tunnel may misbehave.")
		}
	}
}

func displayVersion(v string) string {
	if v == "" || v == "unknown" {
		return "an older build"
	}
	return v
}

// restartAux restarts the panel and bot services if they are installed.
func restartAux() {
	for _, unit := range []string{app.WebUIService, app.BotService} {
		if manage.ServiceStatus(unit) == "active" {
			if err := manage.ControlService("restart", unit); err != nil {
				fail("restart %s: %v", unit, err)
			} else {
				ok("restarted %s", unit)
			}
		}
	}
}

// restartAllTunnels restarts every installed tunnel's engine.
func restartAllTunnels() {
	names, _ := manage.List()
	for _, n := range names {
		if err := manage.Control("restart", n); err != nil {
			fail("restart %s: %v", n, err)
		} else {
			ok("restarted %s", n)
		}
	}
}
