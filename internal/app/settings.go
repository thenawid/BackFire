package app

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	// WebUIConfig stores the web panel settings.
	WebUIConfig = ConfigDir + "/webui.json"
	// TelegramConfig stores the Telegram bot settings.
	TelegramConfig = ConfigDir + "/telegram.json"

	// WebUIService is the systemd unit that runs the web panel.
	WebUIService = "backfire-webui.service"
	// BotService is the systemd unit that runs the Telegram bot.
	BotService = "backfire-bot.service"

	// DefaultWebUIPort is where the panel listens unless told otherwise.
	DefaultWebUIPort = 7777
)

// WebUISettings configures the web panel.
type WebUISettings struct {
	// Port the panel listens on.
	Port int `json:"port"`
	// Password is the login code. Empty disables the panel.
	Password string `json:"password"`
	// ReadOnly hides every control that would change a tunnel, leaving the panel
	// purely a monitor.
	ReadOnly bool `json:"read_only"`
	// TLS serves the panel over HTTPS. With no cert/key a self-signed
	// certificate is generated, so encryption can be turned on without obtaining
	// a CA-signed certificate first.
	TLS bool `json:"tls"`
	// TLSCert / TLSKey are an optional certificate pair; when both are set they
	// are used instead of a self-signed certificate.
	TLSCert string `json:"tls_cert"`
	TLSKey  string `json:"tls_key"`
}

// Scheme returns the URL scheme the panel is reached over.
func (s WebUISettings) Scheme() string {
	if s.TLS {
		return "https"
	}
	return "http"
}

// TelegramSettings configures the bot.
type TelegramSettings struct {
	// Token is the BotFather token.
	Token string `json:"token"`
	// AdminIDs are the Telegram user IDs allowed to talk to the bot. Anyone else
	// is ignored — the bot is a remote control for the server, so it answers
	// only to named operators.
	AdminIDs []int64 `json:"admin_ids"`
	// Alerts turns on unsolicited notifications when a threshold is crossed or a
	// tunnel changes state.
	Alerts bool `json:"alerts"`
	// CPUThreshold / MemThreshold / DiskThreshold are the alert levels in
	// percent. Zero falls back to the defaults.
	CPUThreshold  float64 `json:"cpu_threshold"`
	MemThreshold  float64 `json:"mem_threshold"`
	DiskThreshold float64 `json:"disk_threshold"`
}

// WithDefaults fills unset alert thresholds so a partially written file still
// produces sensible alerting.
func (t TelegramSettings) WithDefaults() TelegramSettings {
	if t.CPUThreshold <= 0 {
		t.CPUThreshold = 85
	}
	if t.MemThreshold <= 0 {
		t.MemThreshold = 90
	}
	if t.DiskThreshold <= 0 {
		t.DiskThreshold = 90
	}
	return t
}

// IsAdmin reports whether a Telegram user may command the bot.
func (t TelegramSettings) IsAdmin(id int64) bool {
	for _, a := range t.AdminIDs {
		if a == id {
			return true
		}
	}
	return false
}

// LoadWebUI reads the panel settings.
func LoadWebUI() (WebUISettings, error) {
	var s WebUISettings
	err := loadJSON(WebUIConfig, &s)
	if s.Port == 0 {
		s.Port = DefaultWebUIPort
	}
	return s, err
}

// SaveWebUI writes the panel settings with owner-only permissions, since they
// contain the login code.
func SaveWebUI(s WebUISettings) error { return saveJSON(WebUIConfig, s) }

// LoadTelegram reads the bot settings.
func LoadTelegram() (TelegramSettings, error) {
	var s TelegramSettings
	err := loadJSON(TelegramConfig, &s)
	return s.WithDefaults(), err
}

// SaveTelegram writes the bot settings with owner-only permissions, since they
// contain the bot token.
func SaveTelegram(s TelegramSettings) error { return saveJSON(TelegramConfig, s) }

func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// saveJSON writes atomically: a temporary file in the same directory, then a
// rename, so a crash mid-write cannot leave a truncated settings file that
// locks the operator out of their own panel.
func saveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
