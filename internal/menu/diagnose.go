package menu

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/app"
	"github.com/thenawid/backfire/internal/manage"
)

// diagnoseTunnel inspects one installed tunnel and reports, in plain terms, why
// it is or is not connected — the service state, whether the engine is
// publishing, whether the peer is linked, and an active reachability probe that
// separates "the far side is unreachable" (firewall / wrong address / server
// down) from "reachable but not linked" (token or transport mismatch).
func diagnoseTunnel() error {
	name, err := pickTunnel("Diagnose which tunnel")
	if err != nil {
		return err
	}
	cfg, err := manage.LoadConfig(name)
	if err != nil {
		return fmt.Errorf("read the tunnel's config: %w", err)
	}

	title("Diagnose: " + name)
	role := string(cfg.Role)
	fam := "backpack"
	if cfg.Family == config.FamilyBackhaul {
		fam = "backhaul"
	}
	field("role", role)
	field("family", fam)

	// 1) Is the systemd service running at all?
	status := manage.Status(name)
	field("service", statusColor(status))
	if status != "active" {
		fail("The service is not running — nothing can connect until it starts.")
		note("Start it:   sudo systemctl start %s", serviceUnit(name))
		note("See why it stopped:")
		note("   journalctl -u %s -n 40 --no-pager", serviceUnit(name))
		if out, e := manage.Logs(name, 12); e == nil && strings.TrimSpace(out) != "" {
			fmt.Println()
			note("last log lines:")
			printIndented(out)
		}
		pause()
		return nil
	}

	// 2) Is the engine alive and publishing state, and is a peer linked?
	st, live := findState(name)
	if !live {
		warn("The service is active but the engine is not publishing state yet.")
		note("Give it a few seconds, or check the logs if this persists:")
		note("   journalctl -u %s -n 40 --no-pager", serviceUnit(name))
	} else {
		field("peer", linkedWord(st.Linked))
		if st.PeerVersion != "" {
			field("peer version", displayVersion(st.PeerVersion))
		}
	}

	// 3) Role/family-specific active checks.
	if cfg.Family == config.FamilyBackhaul {
		diagnoseBackhaul(cfg, role)
	} else if cfg.Role == config.RoleServer {
		diagnoseServerSide(cfg)
	} else {
		diagnoseClientSide(cfg, live && st.Linked)
	}

	fmt.Println()
	pause()
	return nil
}

// diagnoseServerSide checks the exposed side: that something is actually
// listening on the tunnel's bind port.
func diagnoseServerSide(cfg *config.Config) {
	fmt.Println()
	title("Server-side checks")
	_, port, err := net.SplitHostPort(cfg.Server.Bind)
	if err != nil {
		fail("bind address %q is malformed", cfg.Server.Bind)
		return
	}
	field("listen port", port)

	if listening("127.0.0.1:" + port) {
		ok("The tunnel is listening on port %s locally.", port)
		note("If the client abroad still cannot reach it, the cause is outside this")
		note("server: open port %s in the provider firewall / security group, and make", port)
		note("sure the client uses this server's PUBLIC IP with :%s.", port)
	} else {
		fail("Nothing is accepting connections on port %s.", port)
		note("The engine may still be starting, or the port may be taken. Check:")
		note("   sudo ss -ltnp | grep :%s", port)
	}
	if len(cfg.Server.Forwards) == 0 {
		warn("This server has no forwarded ports — even once linked it publishes nothing.")
	} else {
		field("forwards", strings.Join(cfg.Server.Forwards, ", "))
	}
}

// diagnoseClientSide is the most useful probe: it dials the configured server
// address so an unreachable far side is told apart from a reachable-but-rejected
// one.
func diagnoseClientSide(cfg *config.Config, linked bool) {
	fmt.Println()
	title("Client-side checks")
	server := cfg.Client.Server
	field("server address", server)
	if cfg.Client.Token == "" {
		fail("No token is set — the server will reject every attempt.")
		note("Copy the token shown on the server side into this tunnel.")
	}

	note("Dialing the server to see whether it is reachable from here…")
	if reachable(server) {
		ok("The server address is reachable (TCP connect succeeded).")
		if linked {
			ok("And the peer is linked — this tunnel is healthy.")
		} else {
			warn("Reachable, but the peer is not linked. That points at a mismatch:")
			note("  • the TOKEN differs between the two sides, or")
			note("  • the TRANSPORT differs (both ends must use %s), or", cfg.Client.Transport)
			note("  • the server is a different backfire version — check both logs.")
		}
	} else {
		fail("Could not reach %s from this server.", server)
		note("This is almost always one of:")
		note("  • wrong IP or port (must be the server's PUBLIC IP and tunnel port),")
		note("  • a firewall on the server blocking that port, or")
		note("  • the server tunnel is not running.")
	}
}

// diagnoseBackhaul reports what can be checked for a layer-3 tunnel. The raw
// carriers (icmp/ipip/gre/…) have no port to probe, so the guidance differs.
func diagnoseBackhaul(cfg *config.Config, role string) {
	fmt.Println()
	title("Layer-3 (backhaul) checks")
	bh := cfg.Backhaul
	field("carrier", string(bh.Carrier))
	field("tunnel", bh.LocalIP+" ↔ "+bh.RemoteIP)

	if role == string(config.RoleClient) {
		field("peer (dials)", bh.Peer)
		if bh.Peer == "" {
			fail("The client has no peer IP set — it has nothing to dial.")
			return
		}
		if bh.Carrier.NeedsPort() {
			if reachable(fmt.Sprintf("%s:%d", bh.Peer, bh.Port)) {
				ok("The peer's carrier port is reachable.")
			} else {
				fail("Could not reach the peer's %s carrier on port %d.", bh.Carrier, bh.Port)
				note("Check the peer's public IP, the port, and the provider firewall.")
			}
		} else {
			note("The %s carrier rides a raw IP protocol with no TCP port to probe.", bh.Carrier)
			note("Many hosting providers filter raw ICMP/IP protocols — if this never")
			note("links, try the 'udp' or 'tcp' carrier, which are rarely filtered.")
		}
	} else {
		note("This is the server side: it binds and waits for the client's frames.")
		if !bh.Carrier.NeedsPort() {
			note("With a raw carrier, confirm the provider does not filter it; if in doubt")
			note("switch both ends to the 'udp' carrier.")
		}
	}
	note("Both ends must share the same carrier and key, with the tunnel IPs swapped.")
}

// --- probes -----------------------------------------------------------------

// reachable reports whether a TCP connection to addr can be opened quickly.
func reachable(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 4*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// listening reports whether something is already accepting on addr — used to
// confirm the server engine actually bound its port.
func listening(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func serviceUnit(name string) string { return app.ServiceName(name) }

func printIndented(s string) {
	for _, line := range strings.Split(s, "\n") {
		fmt.Printf("    %s%s%s\n", grey, line, reset)
	}
}
