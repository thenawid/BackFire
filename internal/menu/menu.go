// Package menu is the interactive CLI shown when backfire is run with no
// arguments. It creates, inspects and controls tunnels — and the web panel and
// Telegram bot — without the operator ever editing a file or writing a systemd
// unit by hand.
//
// Every question shows, in parentheses, the value that will be used if the
// operator just presses Enter.
package menu

import (
	"fmt"
	"os"
	"strings"

	"github.com/thenawid/backfire/cmd"
	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/app"
	"github.com/thenawid/backfire/internal/manage"
	"github.com/thenawid/backfire/internal/metrics"
	"github.com/thenawid/backfire/internal/sysstat"
	"github.com/thenawid/backfire/internal/utils"
)

func banner() string {
	return blue + `
  ██████╗  █████╗  ██████╗██╗  ██╗███████╗██╗██████╗ ███████╗
  ██╔══██╗██╔══██╗██╔════╝██║ ██╔╝██╔════╝██║██╔══██╗██╔════╝
  ██████╔╝███████║██║     █████╔╝ █████╗  ██║██████╔╝█████╗
  ██╔══██╗██╔══██║██║     ██╔═██╗ ██╔══╝  ██║██╔══██╗██╔══╝
  ██████╔╝██║  ██║╚██████╗██║  ██╗██║     ██║██║  ██║███████╗
  ╚═════╝ ╚═╝  ╚═╝ ╚═════╝╚═╝  ╚═╝╚═╝     ╚═╝╚═╝  ╚═╝╚══════╝` + reset + `
  ` + grey + `reverse tunnel · ` + reset + cyan + app.Version + reset + `
`
}

// Run shows the top-level menu until the operator exits.
func Run() error {
	fmt.Print("\033[2J\033[H") // clear the screen so the panel starts clean
	fmt.Println(banner())

	for {
		overview()
		title("Main menu")
		fmt.Printf("   %s%s Tunnels%s\n", bold, blue, reset)
		fmt.Println("    1) Create a server tunnel   " + grey + "exposed side / Iran" + reset)
		fmt.Println("    2) Create a client tunnel   " + grey + "origin side / abroad" + reset)
		fmt.Println("    3) Manage a tunnel          " + grey + "start, stop, restart, logs" + reset)
		fmt.Println("    4) Show a tunnel's config")
		fmt.Println("    5) Remove a tunnel")
		fmt.Printf("\n   %s%s Services%s\n", bold, blue, reset)
		fmt.Println("    6) Web panel                " + grey + "set up, enable, disable" + reset)
		fmt.Println("    7) Telegram bot             " + grey + "set up, enable, disable" + reset)
		fmt.Printf("\n   %s%s Tools%s\n", bold, blue, reset)
		fmt.Println("    8) Generate a random token")
		fmt.Println("    9) System information")
		fmt.Println("    0) Exit")
		fmt.Println()

		switch ask("Choice", "0") {
		case "1":
			run(createServer)
		case "2":
			run(createClient)
		case "3":
			run(manageTunnel)
		case "4":
			run(showConfig)
		case "5":
			run(removeTunnel)
		case "6":
			run(webUIMenu)
		case "7":
			run(botMenu)
		case "8":
			title("New token")
			field("token", cyan+utils.GenToken(24)+reset)
			note("Both ends of a tunnel must use the same token.")
			pause()
		case "9":
			systemInfo()
		case "0":
			fmt.Println()
			return nil
		default:
			fail("Unknown choice.")
		}
		fmt.Print("\033[2J\033[H")
		fmt.Println(banner())
	}
}

func run(fn func() error) {
	if err := fn(); err != nil {
		fail("%v", err)
		pause()
	}
}

// overview is the compact status block at the top of the menu — the same three
// facts the panel leads with, so the CLI and the browser agree at a glance.
func overview() {
	names, _ := manage.List()
	states := metrics.ReadAll()
	live := map[string]metrics.State{}
	for _, s := range states {
		live[s.Name] = s
	}

	title("Status")
	if len(names) == 0 {
		note("No tunnels installed yet.")
	}
	for _, n := range names {
		status := manage.Status(n)
		st, isLive := live[n]

		// A fresh state file proves the engine is alive, so it outranks systemctl.
		shown := status
		if isLive && status != "active" {
			shown = "running"
		}
		dot := red + "●" + reset
		switch {
		case isLive && st.Linked:
			dot = green + "●" + reset
		case status == "active" || isLive:
			dot = yellow + "●" + reset
		}
		line := fmt.Sprintf("  %s %s%-14s%s %s", dot, bold, n, reset, statusColor(shown))
		if isLive {
			line += fmt.Sprintf("  %s%s  ↓%s ↑%s%s",
				grey, st.Transport,
				sysstat.FormatBytes(st.RxBytes), sysstat.FormatBytes(st.TxBytes), reset)
			if st.PingMs >= 0 {
				line += fmt.Sprintf(" %s%.0fms%s", grey, st.PingMs, reset)
			}
		}
		fmt.Println(line)
	}

	web := manage.ServiceStatus(app.WebUIService)
	bot := manage.ServiceStatus(app.BotService)
	fmt.Printf("  %spanel%s %s   %sbot%s %s\n", grey, reset, statusColor(web), grey, reset, statusColor(bot))
}

// --- tunnels ----------------------------------------------------------------

func createServer() error {
	title("Create a server tunnel")
	note("This is the exposed side — the VPS users connect to.")
	if chooseFamily() {
		return createBackhaul(config.RoleServer)
	}

	name := ask("Tunnel name", "main")
	cfg := cmd.DefaultServerConfig()
	cfg.Server.Bind = "0.0.0.0:" + fmt.Sprint(askInt("Tunnel listen port", 6060))
	cfg.Server.Transport = chooseTransport(cfg.Server.Transport)
	tuneTransport(cfg.Server.Transport, &cfg.Server.Pool, &cfg.Server.KCP)

	title("Forwarded ports")
	note("One per line, as '<published>=<target on the client>'.")
	note("Examples: 443=127.0.0.1:443   ·   2222=127.0.0.1:22   ·   8080")
	note("Press Enter on an empty line when you are done.")
	var forwards []string
	for {
		line := ask(fmt.Sprintf("Forward #%d", len(forwards)+1), "")
		if line == "" {
			break
		}
		f, err := config.ParseForward(line)
		if err != nil {
			fail("%v", err)
			continue
		}
		forwards = append(forwards, line)
		ok("%s → client %s", f.Listen, f.Target)
	}
	if len(forwards) == 0 {
		return fmt.Errorf("a server tunnel needs at least one forward")
	}
	cfg.Server.Forwards = forwards

	if err := manage.Install(name, cfg); err != nil {
		return err
	}

	title("Installed")
	ok("Tunnel '%s' is running and will start on boot.", name)
	fmt.Println()
	field("transport", fmt.Sprintf("%s (%s mode)", cfg.Server.Transport, cfg.Server.Transport.Mode()))
	field("listen port", cfg.Server.Bind)
	field("token", cyan+cfg.Server.Token+reset)
	fmt.Println()
	note("Use this token and the same transport on the client side abroad.")
	pause()
	return nil
}

func createClient() error {
	title("Create a client tunnel")
	note("This is the origin side — the server abroad that holds the real services.")
	if chooseFamily() {
		return createBackhaul(config.RoleClient)
	}

	name := ask("Tunnel name", "main")
	cfg := cmd.DefaultClientConfig()
	cfg.Client.Server = ask("Server address (host:port)", cfg.Client.Server)
	cfg.Client.Transport = chooseTransport(cfg.Client.Transport)
	tuneTransport(cfg.Client.Transport, &cfg.Client.Pool, &cfg.Client.KCP)

	cfg.Client.Token = ask("Token (copied from the server)", "")
	if cfg.Client.Token == "" {
		return fmt.Errorf("token is required — copy it from the server side")
	}
	if cfg.Client.Transport.Base() == config.BaseWSS {
		cfg.Client.TLSVerify = askYesNo("Verify the server's TLS certificate", false)
		if cfg.Client.TLSVerify {
			cfg.Client.ServerName = ask("Certificate name (SNI), blank to derive from the address", "")
		}
	}

	if err := manage.Install(name, cfg); err != nil {
		return err
	}

	title("Installed")
	ok("Tunnel '%s' is running and will start on boot.", name)
	fmt.Println()
	field("transport", fmt.Sprintf("%s (%s mode)", cfg.Client.Transport, cfg.Client.Transport.Mode()))
	field("server", cfg.Client.Server)
	fmt.Println()
	note("It will keep dialing until the server accepts it.")
	pause()
	return nil
}

func manageTunnel() error {
	name, err := pickTunnel("Manage which tunnel")
	if err != nil {
		return err
	}
	for {
		title("Tunnel: " + name)
		field("service", statusColor(manage.Status(name)))
		if st, ok2 := findState(name); ok2 {
			field("transport", st.Transport)
			field("peer", linkedWord(st.Linked))
			field("traffic", "↓ "+sysstat.FormatBytes(st.RxBytes)+"  ↑ "+sysstat.FormatBytes(st.TxBytes))
			if st.PingMs >= 0 {
				field("ping", fmt.Sprintf("%.0f ms", st.PingMs))
			}
			field("connections", st.Conns)
		}
		fmt.Println()
		fmt.Println("    1) Restart")
		fmt.Println("    2) Stop")
		fmt.Println("    3) Start")
		fmt.Println("    4) View recent logs")
		fmt.Println("    0) Back")
		fmt.Println()

		switch ask("Choice", "0") {
		case "1":
			act(manage.Control("restart", name), "restarted")
		case "2":
			act(manage.Control("stop", name), "stopped")
		case "3":
			act(manage.Control("start", name), "started")
		case "4":
			showLogs(name)
		case "0":
			return nil
		default:
			fail("Unknown choice.")
		}
	}
}

func act(err error, verb string) {
	if err != nil {
		fail("%v", err)
	} else {
		ok("Tunnel %s.", verb)
	}
}

func showLogs(name string) {
	title("Logs — " + name)
	out, err := manage.Logs(name, 40)
	if err != nil && out == "" {
		fail("%v", err)
		pause()
		return
	}
	fmt.Println(out)
	pause()
}

func showConfig() error {
	name, err := pickTunnel("Show which tunnel")
	if err != nil {
		return err
	}
	data, err := os.ReadFile(app.ConfigPath(name))
	if err != nil {
		return err
	}
	title("Config — " + name)
	note("%s", app.ConfigPath(name))
	fmt.Println()
	fmt.Println(string(data))
	pause()
	return nil
}

func removeTunnel() error {
	name, err := pickTunnel("Remove which tunnel")
	if err != nil {
		return err
	}
	warn("This deletes the tunnel's config and its systemd unit.")
	if !askYesNo("Remove '"+name+"' permanently", false) {
		note("Nothing was removed.")
		pause()
		return nil
	}
	if err := manage.Remove(name); err != nil {
		return err
	}
	ok("Removed '%s'.", name)
	pause()
	return nil
}

// --- web panel --------------------------------------------------------------

func webUIMenu() error {
	for {
		s, _ := app.LoadWebUI()
		status := manage.ServiceStatus(app.WebUIService)

		title("Web panel")
		field("service", statusColor(status))
		field("port", s.Port)
		if s.Password == "" {
			field("login code", grey+"not set"+reset)
		} else {
			field("login code", cyan+s.Password+reset)
		}
		field("mode", modeWord(s.ReadOnly))
		if s.Password != "" {
			field("url", fmt.Sprintf("http://%s:%d", hostIP(), s.Port))
		}
		fmt.Println()
		fmt.Println("    1) Set up / reconfigure")
		fmt.Println("    2) Restart")
		fmt.Println("    3) Stop and disable")
		fmt.Println("    0) Back")
		fmt.Println()

		switch ask("Choice", "0") {
		case "1":
			if err := setupWebUI(s); err != nil {
				fail("%v", err)
				pause()
			}
		case "2":
			act(manage.ControlService("restart", app.WebUIService), "panel restarted")
			pause()
		case "3":
			if askYesNo("Stop and disable the panel", false) {
				act(manage.RemoveWebUI(), "panel disabled")
				pause()
			}
		case "0":
			return nil
		default:
			fail("Unknown choice.")
		}
	}
}

func setupWebUI(cur app.WebUISettings) error {
	title("Set up the web panel")

	port := askInt("Panel port", orInt(cur.Port, app.DefaultWebUIPort))
	suggested := cur.Password
	if suggested == "" {
		// Suggest a code rather than making the operator invent one — and show
		// it, so pressing Enter is an informed choice.
		suggested = utils.GenToken(4)
	}
	password := ask("Login code", suggested)
	readOnly := askYesNo("Monitoring only (hide every control that changes a tunnel)", cur.ReadOnly)

	s := app.WebUISettings{Port: port, Password: password, ReadOnly: readOnly}
	if err := app.SaveWebUI(s); err != nil {
		return err
	}
	if err := manage.InstallWebUI(); err != nil {
		return err
	}

	title("Panel is live")
	field("url", cyan+fmt.Sprintf("http://%s:%d", hostIP(), port)+reset)
	field("login code", cyan+password+reset)
	field("mode", modeWord(readOnly))
	fmt.Println()
	warn("The port must be open in your firewall for the panel to be reachable.")
	pause()
	return nil
}

// --- telegram bot -----------------------------------------------------------

func botMenu() error {
	for {
		s, _ := app.LoadTelegram()
		status := manage.ServiceStatus(app.BotService)

		title("Telegram bot")
		field("service", statusColor(status))
		field("token", maskToken(s.Token))
		field("admins", adminsWord(s.AdminIDs))
		field("alerts", onOff(s.Alerts))
		if s.Alerts {
			field("thresholds", fmt.Sprintf("cpu %.0f%%  mem %.0f%%  disk %.0f%%",
				s.CPUThreshold, s.MemThreshold, s.DiskThreshold))
		}
		fmt.Println()
		fmt.Println("    1) Set up / reconfigure")
		fmt.Println("    2) Alert thresholds")
		fmt.Println("    3) Restart")
		fmt.Println("    4) Stop and disable")
		fmt.Println("    0) Back")
		fmt.Println()

		switch ask("Choice", "0") {
		case "1":
			if err := setupBot(s); err != nil {
				fail("%v", err)
				pause()
			}
		case "2":
			if err := setupThresholds(s); err != nil {
				fail("%v", err)
				pause()
			}
		case "3":
			act(manage.ControlService("restart", app.BotService), "bot restarted")
			pause()
		case "4":
			if askYesNo("Stop and disable the bot", false) {
				act(manage.RemoveBot(), "bot disabled")
				pause()
			}
		case "0":
			return nil
		default:
			fail("Unknown choice.")
		}
	}
}

func setupBot(cur app.TelegramSettings) error {
	title("Set up the Telegram bot")
	note("Create a bot with @BotFather and paste its token here.")
	note("Get your numeric ID from @userinfobot.")
	fmt.Println()

	token := ask("Bot token", cur.Token)
	if token == "" {
		return fmt.Errorf("a bot token is required")
	}
	idsDefault := joinIDs(cur.AdminIDs)
	idsRaw := ask("Admin Telegram IDs (comma separated)", idsDefault)
	ids, err := parseIDs(idsRaw)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("at least one admin ID is required — without it the bot would answer to anyone")
	}
	alerts := askYesNo("Send alerts when a threshold is crossed or a tunnel drops", true)

	s := cur
	s.Token, s.AdminIDs, s.Alerts = token, ids, alerts
	if err := app.SaveTelegram(s.WithDefaults()); err != nil {
		return err
	}
	if err := manage.InstallBot(); err != nil {
		return err
	}

	title("Bot is live")
	ok("Send /start to your bot on Telegram.")
	field("admins", adminsWord(ids))
	field("alerts", onOff(alerts))
	pause()
	return nil
}

func setupThresholds(cur app.TelegramSettings) error {
	title("Alert thresholds")
	note("An alert is sent once when a value crosses its limit, and once when it recovers.")
	fmt.Println()

	s := cur
	s.CPUThreshold = float64(askInt("CPU threshold %", int(cur.CPUThreshold)))
	s.MemThreshold = float64(askInt("Memory threshold %", int(cur.MemThreshold)))
	s.DiskThreshold = float64(askInt("Disk threshold %", int(cur.DiskThreshold)))
	if err := app.SaveTelegram(s.WithDefaults()); err != nil {
		return err
	}
	// The running bot reads its settings at start-up, so it needs a nudge.
	if manage.ServiceStatus(app.BotService) == "active" {
		_ = manage.ControlService("restart", app.BotService)
	}
	ok("Thresholds saved.")
	pause()
	return nil
}

// --- system info ------------------------------------------------------------

func systemInfo() {
	h := sysstat.NewCollector("/")
	h.Sample() // prime, so CPU is a real difference rather than zero
	stats := h.Sample()

	title("System")
	field("os", stats.OS)
	field("uptime", sysstat.FormatUptime(stats.Uptime))
	field("cores", stats.Cores)
	fmt.Println()
	meter("cpu", stats.CPUPercent, "")
	meter("memory", stats.MemPercent,
		sysstat.FormatBytes(stats.MemUsed)+" / "+sysstat.FormatBytes(stats.MemTotal))
	meter("disk", stats.DiskPercent,
		sysstat.FormatBytes(stats.DiskUsed)+" / "+sysstat.FormatBytes(stats.DiskTotal))
	if stats.SwapTotal > 0 {
		meter("swap", stats.SwapPercent,
			sysstat.FormatBytes(stats.SwapUsed)+" / "+sysstat.FormatBytes(stats.SwapTotal))
	}
	fmt.Println()
	field("network", "↓ "+sysstat.FormatRate(stats.NetRxRate)+"   ↑ "+sysstat.FormatRate(stats.NetTxRate))
	pause()
}

// meter draws a coloured bar that turns amber then red as it fills.
func meter(label string, pct float64, detail string) {
	const width = 24
	filled := int(pct/100*width + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	colour := blue
	switch {
	case pct >= 90:
		colour = red
	case pct >= 75:
		colour = yellow
	}
	bar := colour + strings.Repeat("█", filled) + reset + dim + strings.Repeat("░", width-filled) + reset
	fmt.Printf("  %s%-8s%s %s %5.1f%%  %s%s%s\n", grey, label, reset, bar, pct, grey, detail, reset)
}

// --- shared helpers ---------------------------------------------------------

func chooseTransport(def config.Transport) config.Transport {
	title("Transport")
	names := make([]string, len(config.KnownTransports))
	notes := make([]string, len(config.KnownTransports))
	defIndex := 0
	for i, t := range config.KnownTransports {
		names[i] = string(t)
		notes[i] = t.Describe()
		if t == def {
			defIndex = i
		}
	}
	return config.KnownTransports[askChoice("Transport", names, notes, defIndex)]
}

// tuneTransport asks only the questions that apply to the chosen transport.
func tuneTransport(t config.Transport, p *config.PoolConfig, k *config.KCPConfig) {
	if !t.IsMux() {
		note("%s keeps warm, pre-authenticated links so no connection waits for a handshake.", t)
		p.Size = askInt("Pool size", orInt(p.Size, 8))
		return
	}
	if t == config.KCP {
		note("kcp sends redundant packets so loss is repaired without a retransmit.")
		note("10 data : 3 parity suits most paths; raise parity on a worse one.")
		k.DataShards = askInt("Data shards", orInt(k.DataShards, 10))
		k.ParityShards = askInt("Parity shards", orInt(k.ParityShards, 3))
	}
}

func pickTunnel(label string) (string, error) {
	names, err := manage.List()
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no tunnels installed yet")
	}
	notes := make([]string, len(names))
	for i, n := range names {
		notes[i] = manage.Status(n)
	}
	return names[askChoice(label, names, notes, 0)], nil
}

func findState(name string) (metrics.State, bool) {
	for _, s := range metrics.ReadAll() {
		if s.Name == name {
			return s, true
		}
	}
	return metrics.State{}, false
}

func orInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func onOff(v bool) string {
	if v {
		return green + "on" + reset
	}
	return grey + "off" + reset
}

func modeWord(readOnly bool) string {
	if readOnly {
		return yellow + "monitoring only" + reset
	}
	return green + "full control" + reset
}

func linkedWord(v bool) string {
	if v {
		return green + "connected" + reset
	}
	return red + "not connected" + reset
}

func adminsWord(ids []int64) string {
	if len(ids) == 0 {
		return grey + "none set" + reset
	}
	return joinIDs(ids)
}

func maskToken(t string) string {
	if t == "" {
		return grey + "not set" + reset
	}
	if len(t) <= 10 {
		return strings.Repeat("•", len(t))
	}
	return t[:6] + strings.Repeat("•", 8) + t[len(t)-4:]
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return strings.Join(parts, ", ")
}

func parseIDs(raw string) ([]int64, error) {
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var id int64
		if _, err := fmt.Sscanf(part, "%d", &id); err != nil {
			return nil, fmt.Errorf("“%s” is not a numeric Telegram ID", part)
		}
		out = append(out, id)
	}
	return out, nil
}

func hostIP() string {
	if ip := utils.OutboundIP(); ip != "" {
		return ip
	}
	return "your-server-ip"
}
