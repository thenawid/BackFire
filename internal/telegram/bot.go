package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/thenawid/backfire/internal/app"
	"github.com/thenawid/backfire/internal/backup"
	"github.com/thenawid/backfire/internal/manage"
	"github.com/thenawid/backfire/internal/metrics"
	"github.com/thenawid/backfire/internal/sysstat"
	"github.com/thenawid/backfire/internal/updater"
	"github.com/thenawid/backfire/internal/utils"
)

// Bot is the running Telegram bot.
type Bot struct {
	settings app.TelegramSettings
	log      *utils.Logger
	api      *api
	stats    *sysstat.Collector

	// alerted remembers which conditions have already been reported, so a
	// sustained problem produces one message rather than one every poll.
	alerted map[string]bool
	// lastLinked tracks each tunnel's peer state to detect transitions.
	lastLinked map[string]bool
}

// New builds a bot from its settings.
func New(s app.TelegramSettings, log *utils.Logger) *Bot {
	return &Bot{
		settings:   s.WithDefaults(),
		log:        log.With("telegram"),
		api:        newAPI(s.Token),
		stats:      sysstat.NewCollector("/"),
		alerted:    map[string]bool{},
		lastLinked: map[string]bool{},
	}
}

// commands is the slash-command menu registered with Telegram.
var commands = []botCommand{
	{"status", "every tunnel: state, ports, traffic"},
	{"system", "processor, memory and disk"},
	{"backup", "send a full backup here as a file"},
	{"update", "check for and install a new version"},
	{"alerts", "current alert thresholds"},
	{"webui", "panel link and login code"},
	{"support", "project links"},
}

// Run polls for updates until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	if b.settings.Token == "" {
		return fmt.Errorf("telegram bot has no token set")
	}
	if len(b.settings.AdminIDs) == 0 {
		return fmt.Errorf("telegram bot has no admin IDs — it would answer to anyone")
	}

	me, err := b.api.getMe(ctx)
	if err != nil {
		return fmt.Errorf("verify bot token: %w", err)
	}
	b.log.Infof("connected as @%s", me.Username)

	_ = b.api.deleteWebhook(ctx)
	if err := b.api.setCommands(ctx, commands); err != nil {
		b.log.Warnf("register command menu: %v", err)
	}

	b.stats.Sample() // prime, so the first CPU figure is real
	if b.settings.Alerts {
		go b.alertLoop(ctx)
	}

	var offset int64
	for {
		if ctx.Err() != nil {
			return nil
		}
		updates, err := b.api.getUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.log.Warnf("poll: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.handle(ctx, u)
		}
	}
}

// handle dispatches one update, ignoring anyone who is not an admin.
func (b *Bot) handle(ctx context.Context, u Update) {
	switch {
	case u.CallbackQuery != nil:
		q := u.CallbackQuery
		if q.From == nil || !b.settings.IsAdmin(q.From.ID) {
			_ = b.api.answerCallback(ctx, q.ID, "not authorised")
			return
		}
		_ = b.api.answerCallback(ctx, q.ID, "")
		if q.Message == nil || q.Message.Chat == nil {
			return
		}
		text, kb := b.render(ctx, q.Data)
		// Editing in place keeps one live panel instead of a growing transcript.
		if err := b.api.edit(ctx, q.Message.Chat.ID, q.Message.MessageID, text, kb); err != nil {
			_ = b.api.send(ctx, q.Message.Chat.ID, text, kb)
		}

	case u.Message != nil:
		m := u.Message
		if m.From == nil || m.Chat == nil {
			return
		}
		if !b.settings.IsAdmin(m.From.ID) {
			b.log.Warnf("ignoring message from non-admin %d (@%s)", m.From.ID, m.From.Username)
			_ = b.api.send(ctx, m.Chat.ID,
				"This bot is private.\nYour Telegram ID is <code>"+fmt.Sprint(m.From.ID)+"</code>.", nil)
			return
		}
		cmd := strings.TrimPrefix(strings.Fields(m.Text + " ")[0], "/")
		if i := strings.Index(cmd, "@"); i >= 0 {
			cmd = cmd[:i] // strip the @botname suffix used in groups
		}
		text, kb := b.render(ctx, cmd)
		if cmd == "backup" {
			b.sendBackup(ctx, m.Chat.ID)
			return
		}
		_ = b.api.send(ctx, m.Chat.ID, text, kb)
	}
}

// mainKeyboard is the button grid attached to every panel message.
func mainKeyboard() *InlineKeyboard {
	return &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{{Text: "📊 Status", Data: "status"}, {Text: "🖥 System", Data: "system"}},
		{{Text: "🔐 Backup", Data: "backup"}, {Text: "⟳ Update", Data: "update"}},
		{{Text: "🔔 Alerts", Data: "alerts"}, {Text: "🌐 Panel", Data: "webui"}},
		{{Text: "💙 Support", Data: "support"}},
	}}
}

// updateKeyboard offers to install when a newer version is available, plus a way
// back to the main panel.
func updateKeyboard(canInstall bool) *InlineKeyboard {
	rows := [][]InlineButton{}
	if canInstall {
		rows = append(rows, []InlineButton{{Text: "⬇ Install now", Data: "update_apply"}})
	}
	rows = append(rows, []InlineButton{{Text: "⟳ Re-check", Data: "update"}, {Text: "« Back", Data: "menu"}})
	return &InlineKeyboard{InlineKeyboard: rows}
}

// render turns a command into the message body and keyboard to show.
func (b *Bot) render(ctx context.Context, cmd string) (string, *InlineKeyboard) {
	switch cmd {
	case "status":
		return b.statusText(), mainKeyboard()
	case "system":
		return b.systemText(), mainKeyboard()
	case "alerts":
		return b.alertsText(), mainKeyboard()
	case "webui":
		return b.webuiText(), mainKeyboard()
	case "support":
		return supportText(), mainKeyboard()
	case "backup":
		return "Preparing a backup…", mainKeyboard()
	case "update":
		return b.updateText(ctx)
	case "update_apply":
		return b.updateApplyText(ctx)
	default:
		return b.welcomeText(), mainKeyboard()
	}
}

// updateText checks GitHub for a newer release and reports what it finds,
// offering an install button when an update is available. It changes nothing.
func (b *Bot) updateText(ctx context.Context) (string, *InlineKeyboard) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	chk, err := updater.Check(cctx)
	if err != nil {
		return "<b>⟳ Update</b>\n\nCould not check for updates:\n<code>" + esc(err.Error()) + "</code>", updateKeyboard(false)
	}
	var s strings.Builder
	s.WriteString("<b>⟳ Update</b>\n\n")
	s.WriteString("Installed : <code>" + esc(chk.Current.Raw) + "</code>\n")
	s.WriteString("Latest : <code>" + esc(chk.Latest.Raw) + "</code>\n")
	s.WriteString(b.stalePeerText(chk.Latest))
	if !chk.Available {
		s.WriteString("\n✅ Already on the latest version.")
		return s.String(), updateKeyboard(false)
	}
	s.WriteString("\nA new version is available. Installing replaces the binary in " +
		"place — running tunnels are <b>not</b> interrupted and keep the old version " +
		"until you restart them.")
	return s.String(), updateKeyboard(true)
}

// updateApplyText installs the latest release. Running tunnels are not dropped.
func (b *Bot) updateApplyText(ctx context.Context) (string, *InlineKeyboard) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := updater.Update(cctx)
	if err != nil {
		return "<b>⟳ Update</b>\n\nUpdate failed:\n<code>" + esc(err.Error()) + "</code>", updateKeyboard(false)
	}
	if res.UpToDate {
		return "<b>⟳ Update</b>\n\n✅ Already up to date (<code>" + esc(res.Current.Raw) + "</code>).", updateKeyboard(false)
	}
	b.log.Infof("updated %s -> %s via telegram (tunnels not interrupted)", res.Current.Raw, res.Latest.Raw)
	var s strings.Builder
	s.WriteString("<b>✅ Updated</b>\n\n")
	s.WriteString("Installed <code>" + esc(res.Latest.Raw) + "</code> (was <code>" + esc(res.Current.Raw) + "</code>).\n\n")
	s.WriteString("Running tunnels were not interrupted. Restart the panel/bot services " +
		"and the tunnels when convenient to run the new version.")
	return s.String(), updateKeyboard(false)
}

// stalePeerText warns about linked peers far behind the given version, so both
// ends of a tunnel get updated together.
func (b *Bot) stalePeerText(latest updater.Version) string {
	var s strings.Builder
	for _, st := range metrics.ReadAll() {
		if !st.Linked || st.PeerVersion == "" {
			continue
		}
		if updater.MuchOlder(updater.Parse(st.PeerVersion), latest) {
			v := st.PeerVersion
			if v == "" || v == "unknown" {
				v = "an older build"
			}
			s.WriteString(fmt.Sprintf("\n⚠️ Peer of <b>%s</b> is on <code>%s</code> — update the OTHER server too.\n",
				esc(st.Name), esc(v)))
		}
	}
	return s.String()
}

func (b *Bot) welcomeText() string {
	var s strings.Builder
	s.WriteString("<b>🔥 backfire " + app.Version + "</b>\n\n")
	for _, c := range commands {
		s.WriteString("/" + c.Command + " — " + c.Description + "\n")
	}
	s.WriteString("\nAlerts arrive on their own when a threshold is crossed or a tunnel changes state.")
	return s.String()
}

// statusText is the per-tunnel report.
func (b *Bot) statusText() string {
	states := metrics.ReadAll()
	byName := map[string]metrics.State{}
	for _, s := range states {
		byName[s.Name] = s
	}
	names, _ := manage.List()
	for _, s := range states {
		if !contains(names, s.Name) {
			names = append(names, s.Name)
		}
	}
	if len(names) == 0 {
		return "No tunnels installed yet.\nCreate one with <code>sudo backfire</code>."
	}

	var s strings.Builder
	for _, name := range names {
		st, live := byName[name]
		status := manage.Status(name)
		// A fresh state file proves the engine is alive, so it outranks systemctl.
		running := status == "active" || live

		// Three states worth distinguishing, same as the panel.
		icon := "🔴"
		switch {
		case live && st.Linked:
			icon = "🟢"
		case running:
			icon = "🟡"
		}
		s.WriteString(fmt.Sprintf("%s <b>%s</b>", icon, esc(name)))
		if st.Transport != "" {
			s.WriteString(fmt.Sprintf(" [ %s ]", strings.ToUpper(esc(st.Transport))))
		}
		s.WriteString("\n")

		if st.Port != 0 {
			s.WriteString(fmt.Sprintf("Tunnel Port : %d\n", st.Port))
		}
		if len(st.Forwarded) > 0 {
			s.WriteString("Forwarded Port : " + joinInts(st.Forwarded) + "\n")
		}
		if live {
			s.WriteString(fmt.Sprintf("↑ %s | ↓ %s | Σ %s\n",
				sysstat.FormatBytes(st.TxBytes),
				sysstat.FormatBytes(st.RxBytes),
				sysstat.FormatBytes(st.Total)))
			if st.PingMs >= 0 {
				s.WriteString(fmt.Sprintf("Ping : %.0f ms\n", st.PingMs))
			}
			if st.Conns > 0 {
				s.WriteString(fmt.Sprintf("Connections : %d\n", st.Conns))
			}
		} else {
			s.WriteString("Service : " + esc(status) + "\n")
		}
		s.WriteString("\n")
	}
	return strings.TrimRight(s.String(), "\n")
}

// systemText is the host report, with the little bar meters from the panel.
func (b *Bot) systemText() string {
	h := b.stats.Sample()
	var s strings.Builder
	s.WriteString("<b>🖥 System</b>\n\n")
	s.WriteString("OS : " + esc(h.OS) + "\n")
	s.WriteString("UpTime : " + sysstat.FormatUptime(h.Uptime) + "\n\n")
	s.WriteString(fmt.Sprintf("<code>%s</code> CPU %.1f%%\n", bar(h.CPUPercent), h.CPUPercent))
	s.WriteString(fmt.Sprintf("<code>%s</code> Memory %.1f%%\n", bar(h.MemPercent), h.MemPercent))
	s.WriteString(fmt.Sprintf("<code>%s</code> Disk %.1f%%\n", bar(h.DiskPercent), h.DiskPercent))
	if h.SwapTotal > 0 {
		s.WriteString(fmt.Sprintf("<code>%s</code> Swap %.1f%%\n", bar(h.SwapPercent), h.SwapPercent))
	}
	s.WriteString("\n")
	s.WriteString("Memory : " + sysstat.FormatBytes(h.MemUsed) + " / " + sysstat.FormatBytes(h.MemTotal) + "\n")
	s.WriteString("Disk : " + sysstat.FormatBytes(h.DiskUsed) + " / " + sysstat.FormatBytes(h.DiskTotal) + "\n")
	s.WriteString("Cores : " + fmt.Sprint(h.Cores) + "\n")
	s.WriteString("Network : ↓ " + sysstat.FormatRate(h.NetRxRate) + " · ↑ " + sysstat.FormatRate(h.NetTxRate))
	return s.String()
}

func (b *Bot) alertsText() string {
	state := "off"
	if b.settings.Alerts {
		state = "on"
	}
	return fmt.Sprintf("<b>🔔 Alerts — %s</b>\n\n"+
		"CPU : above %.0f%%\n"+
		"Memory : above %.0f%%\n"+
		"Disk : above %.0f%%\n\n"+
		"A tunnel losing or regaining its peer is always reported.\n"+
		"Change these with <code>sudo backfire</code>.",
		state, b.settings.CPUThreshold, b.settings.MemThreshold, b.settings.DiskThreshold)
}

func (b *Bot) webuiText() string {
	w, err := app.LoadWebUI()
	if err != nil || w.Password == "" {
		return "<b>🌐 Web panel</b>\n\nNot set up yet.\nEnable it with <code>sudo backfire</code>."
	}
	host := "your-server-ip"
	if ip := utils.OutboundIP(); ip != "" {
		host = ip
	}
	mode := "full control"
	if w.ReadOnly {
		mode = "monitoring only"
	}
	return fmt.Sprintf("<b>🌐 Web panel</b>\n\nURL : %s://%s:%d\nLogin code : <code>%s</code>\nMode : %s",
		w.Scheme(), host, w.Port, esc(w.Password), mode)
}

func supportText() string {
	return "<b>💙 backfire</b>\n\n" +
		"An open-source reverse tunnel.\n" +
		"github.com/" + app.RepoOwner + "/" + app.RepoName + "\n\n" +
		"Issues and pull requests are welcome."
}

// sendBackup tars every config and setting and uploads it.
func (b *Bot) sendBackup(ctx context.Context, chatID int64) {
	data, err := backup.Build()
	if err != nil {
		_ = b.api.send(ctx, chatID, "Backup failed: "+esc(err.Error()), mainKeyboard())
		return
	}
	name := fmt.Sprintf("backfire-backup-%s.tar.gz", time.Now().Format("2006-01-02-1504"))
	caption := "<b>🔐 Backup</b>\nEvery tunnel config and panel/bot setting.\n" +
		"⚠️ Contains live tokens — keep it private."
	if err := b.api.sendDocument(ctx, chatID, name, data, caption); err != nil {
		_ = b.api.send(ctx, chatID, "Could not upload the backup: "+esc(err.Error()), mainKeyboard())
	}
}

// --- alerts -----------------------------------------------------------------

// alertLoop watches thresholds and tunnel state, messaging every admin when
// something crosses or changes. Each condition fires once per transition, not
// once per check.
func (b *Bot) alertLoop(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.checkAlerts(ctx)
		}
	}
}

func (b *Bot) checkAlerts(ctx context.Context) {
	h := b.stats.Sample()
	b.threshold(ctx, "cpu", h.CPUPercent, b.settings.CPUThreshold, "CPU")
	b.threshold(ctx, "mem", h.MemPercent, b.settings.MemThreshold, "Memory")
	b.threshold(ctx, "disk", h.DiskPercent, b.settings.DiskThreshold, "Disk")

	for _, s := range metrics.ReadAll() {
		was, seen := b.lastLinked[s.Name]
		b.lastLinked[s.Name] = s.Linked
		if !seen || was == s.Linked {
			continue
		}
		if s.Linked {
			b.broadcast(ctx, fmt.Sprintf("🟢 <b>%s</b> — peer reconnected.", esc(s.Name)))
		} else {
			b.broadcast(ctx, fmt.Sprintf("🔴 <b>%s</b> — peer lost.", esc(s.Name)))
		}
	}
}

// threshold reports a crossing once, and reports the recovery once.
func (b *Bot) threshold(ctx context.Context, key string, value, limit float64, label string) {
	if value >= limit {
		if !b.alerted[key] {
			b.alerted[key] = true
			b.broadcast(ctx, fmt.Sprintf("⚠️ <b>%s at %.1f%%</b> (threshold %.0f%%)", label, value, limit))
		}
		return
	}
	// Hysteresis: only clear once it has fallen a few points below the limit, so
	// a value hovering at the line does not flap.
	if b.alerted[key] && value < limit-5 {
		b.alerted[key] = false
		b.broadcast(ctx, fmt.Sprintf("✅ <b>%s back to %.1f%%</b>", label, value))
	}
}

func (b *Bot) broadcast(ctx context.Context, text string) {
	for _, id := range b.settings.AdminIDs {
		if err := b.api.send(ctx, id, text, nil); err != nil {
			b.log.Warnf("alert to %d: %v", id, err)
		}
	}
}

// --- helpers ----------------------------------------------------------------

// bar renders a ten-cell meter for the system report.
func bar(pct float64) string {
	const cells = 10
	filled := int(pct/100*cells + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > cells {
		filled = cells
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", cells-filled)
}

// esc escapes the three characters Telegram's HTML parse mode cares about, so a
// tunnel name can never break the message or inject markup.
func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func joinInts(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
