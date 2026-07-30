// Package menu is the interactive CLI shown when backfire is run with no
// arguments. It creates, inspects and controls tunnels without the operator
// ever editing a TOML file or writing a systemd unit by hand.
package menu

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/thenawid/backfire/cmd"
	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/app"
	"github.com/thenawid/backfire/internal/manage"
	"github.com/thenawid/backfire/internal/utils"
)

const banner = `
  ____             _     __ _
 | __ )  __ _  ___| | __/ _(_)_ __ ___
 |  _ \ / _` + "`" + ` |/ __| |/ / |_| | '__/ _ \
 | |_) | (_| | (__|   <|  _| | | |  __/
 |____/ \__,_|\___|_|\_\_| |_|_|  \___|

 backfire ` + app.Version + ` — reverse tunnel
`

var in = bufio.NewReader(os.Stdin)

// Run shows the top-level menu loop until the operator exits.
func Run() error {
	fmt.Print(banner)
	for {
		fmt.Println()
		fmt.Println("  1) Create server tunnel (exposed side / Iran)")
		fmt.Println("  2) Create client tunnel (origin side / abroad)")
		fmt.Println("  3) List tunnels")
		fmt.Println("  4) Start / stop / restart a tunnel")
		fmt.Println("  5) Show a tunnel's config")
		fmt.Println("  6) Remove a tunnel")
		fmt.Println("  7) Generate a random token")
		fmt.Println("  0) Exit")
		switch prompt("Choice", "0") {
		case "1":
			run(createServer)
		case "2":
			run(createClient)
		case "3":
			listTunnels()
		case "4":
			run(controlTunnel)
		case "5":
			run(showConfig)
		case "6":
			run(removeTunnel)
		case "7":
			fmt.Printf("  token: %s\n", utils.GenToken(24))
		case "0":
			return nil
		default:
			fmt.Println("  unknown choice")
		}
	}
}

func run(fn func() error) {
	if err := fn(); err != nil {
		fmt.Printf("  ! %v\n", err)
	}
}

func createServer() error {
	name := prompt("Tunnel name", "main")
	cfg := cmd.DefaultServerConfig()
	cfg.Server.Bind = "0.0.0.0:" + prompt("Tunnel listen port", "6060")
	cfg.Server.Transport = chooseTransport(cfg.Server.Transport)
	tuneTransport(cfg.Server.Transport, &cfg.Server.Pool, &cfg.Server.KCP)

	fmt.Println("  Forwards — one per line as '<listen>=<target>' (e.g. 443=127.0.0.1:443).")
	fmt.Println("  Empty line finishes.")
	var forwards []string
	for {
		line := prompt("  forward", "")
		if line == "" {
			break
		}
		if _, err := config.ParseForward(line); err != nil {
			fmt.Printf("    ! %v\n", err)
			continue
		}
		forwards = append(forwards, line)
	}
	if len(forwards) == 0 {
		return fmt.Errorf("a server tunnel needs at least one forward")
	}
	cfg.Server.Forwards = forwards

	if err := manage.Install(name, cfg); err != nil {
		return err
	}
	fmt.Printf("\n  installed and started '%s'.\n", name)
	fmt.Printf("  transport : %s (%s mode)\n", cfg.Server.Transport, cfg.Server.Transport.Mode())
	fmt.Printf("  token     : %s\n", cfg.Server.Token)
	fmt.Println("  Use this token and transport when you create the client tunnel abroad.")
	return nil
}

func createClient() error {
	name := prompt("Tunnel name", "main")
	cfg := cmd.DefaultClientConfig()
	cfg.Client.Server = prompt("Server address (host:port)", cfg.Client.Server)
	cfg.Client.Transport = chooseTransport(cfg.Client.Transport)
	tuneTransport(cfg.Client.Transport, &cfg.Client.Pool, &cfg.Client.KCP)
	cfg.Client.Token = prompt("Token (from the server)", "")
	if cfg.Client.Token == "" {
		return fmt.Errorf("token is required")
	}
	if cfg.Client.Transport.Base() == config.BaseWSS {
		cfg.Client.TLSVerify = yesNo("Verify the server TLS certificate?", false)
	}
	if err := manage.Install(name, cfg); err != nil {
		return err
	}
	fmt.Printf("\n  installed and started '%s'. It will keep dialing %s until it links.\n",
		name, cfg.Client.Server)
	return nil
}

func listTunnels() {
	names, err := manage.List()
	if err != nil {
		fmt.Printf("  ! %v\n", err)
		return
	}
	if len(names) == 0 {
		fmt.Println("  no tunnels installed")
		return
	}
	fmt.Println()
	for _, n := range names {
		role := "?"
		if c, err := manage.LoadConfig(n); err == nil {
			role = string(c.Role)
		}
		fmt.Printf("  %-16s %-7s %s\n", n, role, manage.Status(n))
	}
}

func controlTunnel() error {
	name, err := pickTunnel()
	if err != nil {
		return err
	}
	verb := prompt("Action (start/stop/restart)", "restart")
	switch verb {
	case "start", "stop", "restart":
		return manage.Control(verb, name)
	default:
		return fmt.Errorf("unknown action %q", verb)
	}
}

func showConfig() error {
	name, err := pickTunnel()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(app.ConfigPath(name))
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Println(string(data))
	return nil
}

func removeTunnel() error {
	name, err := pickTunnel()
	if err != nil {
		return err
	}
	if !yesNo(fmt.Sprintf("Remove '%s' permanently?", name), false) {
		return nil
	}
	return manage.Remove(name)
}

// pickTunnel lists installed tunnels and asks the operator to choose one.
func pickTunnel() (string, error) {
	names, err := manage.List()
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no tunnels installed")
	}
	for i, n := range names {
		fmt.Printf("  %d) %s\n", i+1, n)
	}
	choice := prompt("Number", "1")
	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 1 || idx > len(names) {
		return "", fmt.Errorf("invalid selection %q", choice)
	}
	return names[idx-1], nil
}

// tuneTransport asks only the questions that actually apply to the chosen
// transport: pool size for the pooled ones, error correction for kcp. Everything
// else keeps its tuned default.
func tuneTransport(t config.Transport, p *config.PoolConfig, k *config.KCPConfig) {
	if !t.IsMux() {
		fmt.Printf("  %s keeps a pool of warm, pre-authenticated links so no\n", t)
		fmt.Println("  connection waits for a dial or handshake.")
		if n, err := strconv.Atoi(prompt("  Pool size", "8")); err == nil && n > 0 {
			p.Size = n
		}
		return
	}
	if t == config.KCP {
		fmt.Println("  kcp can send redundant packets so loss is repaired without a")
		fmt.Println("  retransmit. 10 data : 3 parity suits most paths; raise parity on a")
		fmt.Println("  worse one, at proportional bandwidth cost.")
		if n, err := strconv.Atoi(prompt("  Data shards", "10")); err == nil && n > 0 {
			k.DataShards = n
		}
		if n, err := strconv.Atoi(prompt("  Parity shards", "3")); err == nil && n > 0 {
			k.ParityShards = n
		}
	}
}

// chooseTransport lists every transport with a one-line explanation and accepts
// either its number or its name.
func chooseTransport(def config.Transport) config.Transport {
	fmt.Println()
	fmt.Println("  Transports:")
	for i, t := range config.KnownTransports {
		marker := " "
		if t == def {
			marker = "*"
		}
		fmt.Printf("   %s %2d) %-8s %s\n", marker, i+1, t, t.Describe())
	}
	val := prompt("Transport (number or name)", string(def))

	// A number picks by position in the list.
	if n, err := strconv.Atoi(val); err == nil {
		if n >= 1 && n <= len(config.KnownTransports) {
			return config.KnownTransports[n-1]
		}
		fmt.Printf("  (no transport %d, keeping %s)\n", n, def)
		return def
	}
	for _, t := range config.KnownTransports {
		if string(t) == val {
			return t
		}
	}
	fmt.Printf("  (unknown transport %q, keeping %s)\n", val, def)
	return def
}

// prompt reads a trimmed line, returning def when the operator just hits enter.
func prompt(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func yesNo(label string, def bool) bool {
	d := "n"
	if def {
		d = "y"
	}
	ans := strings.ToLower(prompt(label+" (y/n)", d))
	return ans == "y" || ans == "yes"
}
