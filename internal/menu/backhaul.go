package menu

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/thenawid/backfire/cmd"
	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/manage"
	"github.com/thenawid/backfire/internal/utils"
)

// validTunnelName rejects anything that could not be a safe systemd unit name.
func validTunnelName(s string) error {
	if len(s) > 64 {
		return fmt.Errorf("the name is too long (max 64 characters)")
	}
	for _, r := range s {
		if !(r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("“%c” is not allowed — use letters, digits, - and _ only", r)
		}
	}
	return nil
}

// validIPAddr insists on a real IP literal, used for the private tunnel
// endpoints that are always numeric.
func validIPAddr(s string) error {
	if net.ParseIP(s) == nil {
		return fmt.Errorf("“%s” is not a valid IP address (e.g. 10.200.0.1)", s)
	}
	return nil
}

// validHost accepts an IP or a plausible hostname for the public peer address.
func validHost(s string) error {
	if s == "" || strings.ContainsAny(s, " \t/\\") {
		return fmt.Errorf("“%s” is not a valid host or IP address", s)
	}
	return nil
}

// validHostPort insists on a "host:port" pair with a port in range, so a client
// cannot be created pointing at an address it can never dial.
func validHostPort(s string) error {
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" {
		return fmt.Errorf("“%s” must be host:port, e.g. 203.0.113.5:6060", s)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("“%s” has an invalid port", s)
	}
	return nil
}

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

	name := askValid("Tunnel name", suggestedName(), validTunnelName)
	if name == "" {
		return fmt.Errorf("a tunnel name is required")
	}
	cfg := cmd.DefaultBackhaulConfig(role)
	bh := &cfg.Backhaul

	bh.Carrier = chooseCarrier(bh.Carrier)

	if role == config.RoleClient {
		bh.Peer = askValid("Foreign server IP (the peer this side dials)", "", validHost)
		if bh.Peer == "" {
			return fmt.Errorf("the client side needs the peer's public IP")
		}
	} else {
		// The server may learn the peer from the first packet, so a blank answer
		// is allowed; anything typed must still be a valid address.
		bh.Peer = askOptionalValid("Peer IP (blank = learn it from the first packet)", validHost)
	}

	if bh.Carrier.NeedsPort() {
		bh.Port = askInt("Carrier port", orInt(bh.Port, 2000))
	}

	// The token must match on both ends: the server suggests a fresh one and
	// shows it; the client pastes it.
	if role == config.RoleServer {
		bh.Token = ask("Shared key / PSK", bh.Token)
	} else {
		bh.Token = askRequired("Shared key / PSK (copied from the server)")
		if bh.Token == "" {
			return fmt.Errorf("the shared key is required")
		}
	}

	title("Tunnel addresses")
	note("Private point-to-point addresses for the two ends of the link.")
	bh.LocalIP = askValid("This end's tunnel IP", bh.LocalIP, validIPAddr)
	bh.RemoteIP = askValid("Peer's tunnel IP", bh.RemoteIP, func(s string) error {
		if err := validIPAddr(s); err != nil {
			return err
		}
		if s == bh.LocalIP {
			return fmt.Errorf("the two ends need different tunnel IPs")
		}
		return nil
	})

	if bh.Carrier.IsRaw() {
		bh.Spoof = askYesNo("Enable IP spoofing (forged source address)", false)
		if bh.Spoof {
			bh.SpoofSource = askOptionalValid("Spoofed source IP (blank = random each restart)", validIPAddr)
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
	if bh.Carrier.NeedsPort() {
		field("carrier port", fmt.Sprintf("%d", bh.Port))
	} else {
		field("carrier port", grey+"none — "+string(bh.Carrier)+" has no port, like ping"+reset)
	}
	if role == config.RoleServer {
		field("token", cyan+bh.Token+reset)
	}

	// The single most common confusion is what to do on the other end, so spell
	// it out explicitly rather than leaving the operator to work it out.
	fmt.Println()
	if role == config.RoleServer {
		title("Now set up the OTHER server (abroad)")
		note("Create a CLIENT layer-3 tunnel there with exactly these values:")
		field("  carrier", string(bh.Carrier))
		field("  token", cyan+bh.Token+reset)
		peer := "this server's PUBLIC IP"
		if ip := utils.OutboundIP(); ip != "" {
			peer = ip + "  (this server's public IP)"
		}
		field("  peer / server IP", peer)
		field("  this end's IP", bh.RemoteIP)
		field("  peer's IP", bh.LocalIP)
		if bh.Carrier.NeedsPort() {
			field("  carrier port", fmt.Sprintf("%d", bh.Port))
		}
		fmt.Println()
		note("Then test from the other server:   ping %s", bh.LocalIP)
	} else {
		note("The tunnel is up once the server side is running with the same carrier")
		note("and token. Test with:   ping %s", bh.RemoteIP)
	}

	if bh.Carrier == config.CarrierICMP {
		fmt.Println()
		warn("Many hosting providers filter ICMP. If it never links, switch BOTH")
		warn("ends to the 'udp' (or 'tcp') carrier — those use a port and are")
		warn("rarely filtered. Check with Tools → Diagnose a tunnel.")
	}
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
