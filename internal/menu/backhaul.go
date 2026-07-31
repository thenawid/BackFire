package menu

import (
	"fmt"

	"github.com/thenawid/backfire/cmd"
	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/manage"
)

// chooseFamily asks whether a new tunnel is a BackPack transport tunnel or a
// Backhaul layer-3 tunnel. It returns true for Backhaul.
func chooseFamily() bool {
	names := []string{"BackPack", "Backhaul"}
	notes := []string{
		"reverse tunnel, forwards TCP over one of nine transports",
		"layer-3 TUN/IPX tunnel with camouflage carriers and IP spoof",
	}
	return askChoice("Family", names, notes, 0) == 1
}

// chooseCarrier lists every Backhaul carrier with a one-line explanation and
// accepts either its number or its name.
func chooseCarrier(def config.Carrier) config.Carrier {
	title("Carrier")
	note("The protocol the encrypted layer-3 frames hide inside.")
	names := make([]string, len(config.KnownCarriers))
	notes := make([]string, len(config.KnownCarriers))
	defIndex := 0
	for i, c := range config.KnownCarriers {
		names[i] = string(c)
		notes[i] = c.Describe()
		if c == def {
			defIndex = i
		}
	}
	return config.KnownCarriers[askChoice("Carrier", names, notes, defIndex)]
}

// createBackhaul builds a layer-3 tunnel for the given role. The server (Iran)
// binds and waits; the client (abroad) dials the peer.
func createBackhaul(role config.Role) error {
	title("Create a backhaul tunnel — layer 3")
	note("A TUN device carried inside another IP protocol, with optional spoofing.")
	warn("Backhaul needs root and CAP_NET_ADMIN/CAP_NET_RAW on both servers.")

	name := ask("Tunnel name", "main")
	cfg := cmd.DefaultBackhaulConfig(role)
	bh := &cfg.Backhaul

	bh.Carrier = chooseCarrier(bh.Carrier)

	if role == config.RoleClient {
		bh.Peer = ask("Foreign server IP (the peer this side dials)", "")
		if bh.Peer == "" {
			return fmt.Errorf("the client side needs the peer's public IP")
		}
	} else {
		bh.Peer = ask("Peer IP (blank = learn it from the first packet)", "")
	}

	if bh.Carrier.NeedsPort() {
		bh.Port = askInt("Carrier port", orInt(bh.Port, 2000))
	}

	// The token must match on both ends: the server suggests a fresh one and
	// shows it; the client pastes it.
	if role == config.RoleServer {
		bh.Token = ask("Shared key / PSK", bh.Token)
	} else {
		bh.Token = ask("Shared key / PSK (copied from the server)", "")
		if bh.Token == "" {
			return fmt.Errorf("the shared key is required")
		}
	}

	title("Tunnel addresses")
	note("Private point-to-point addresses for the two ends of the link.")
	bh.LocalIP = ask("This end's tunnel IP", bh.LocalIP)
	bh.RemoteIP = ask("Peer's tunnel IP", bh.RemoteIP)

	if bh.Carrier.IsRaw() {
		bh.Spoof = askYesNo("Enable IP spoofing (forged source address)", false)
		if bh.Spoof {
			bh.SpoofSource = ask("Spoofed source IP (blank = random each restart)", "")
		}
	}

	// Optional port forwards over the established tunnel.
	title("Forwarded ports (optional)")
	note("Publish ports over the tunnel, e.g. 54311 or 443=10.200.0.2:443.")
	note("Press Enter on an empty line to skip / finish.")
	var forwards []string
	for {
		line := ask(fmt.Sprintf("Forward #%d", len(forwards)+1), "")
		if line == "" {
			break
		}
		if _, err := config.ParseForward(line); err != nil {
			fail("%v", err)
			continue
		}
		forwards = append(forwards, line)
		ok("added %s", line)
	}
	bh.Forwards = forwards

	// Advanced escape hatch, mirroring the panel's "raw config".
	if askYesNo("Add raw advanced config lines", false) {
		note("Enter free-form key=value lines; blank line finishes.")
		var raw string
		for {
			line := ask("  raw", "")
			if line == "" {
				break
			}
			raw += line + "\n"
		}
		bh.Raw = raw
	}

	if err := manage.Install(name, cfg); err != nil {
		return err
	}

	title("Installed")
	ok("Backhaul tunnel '%s' is running and will start on boot.", name)
	fmt.Println()
	field("carrier", string(bh.Carrier)+carrierSpoofNote(*bh))
	field("tunnel", bh.LocalIP+" ↔ "+bh.RemoteIP)
	if role == config.RoleServer {
		field("token", cyan+bh.Token+reset)
	}
	fmt.Println()
	note("Use the same carrier and key on the peer, with the tunnel IPs swapped.")
	pause()
	return nil
}

func carrierSpoofNote(b config.BackhaulConfig) string {
	if b.Spoof {
		if b.SpoofSource != "" {
			return grey + " (spoof: " + b.SpoofSource + ")" + reset
		}
		return grey + " (spoof: random)" + reset
	}
	return ""
}
