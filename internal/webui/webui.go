// Package webui serves the browser panel: host vitals, per-tunnel traffic and
// throughput charts, logs, and (unless read-only) tunnel controls.
//
// The whole UI is one embedded HTML file with no external assets, so the panel
// works on a server with no outbound internet access and nothing to fetch from a
// CDN that may be unreachable from where it is deployed.
package webui

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/thenawid/backfire/internal/app"
	"github.com/thenawid/backfire/internal/manage"
	"github.com/thenawid/backfire/internal/metrics"
	"github.com/thenawid/backfire/internal/sysstat"
	"github.com/thenawid/backfire/internal/tlsutil"
	"github.com/thenawid/backfire/internal/utils"
)

//go:embed panel.html
var panelHTML string

// sessionTTL is how long a login lasts before the code must be entered again.
const sessionTTL = 12 * time.Hour

// Server is the running web panel.
type Server struct {
	settings app.WebUISettings
	log      *utils.Logger
	stats    *sysstat.Collector

	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
}

// New builds a panel server.
func New(s app.WebUISettings, log *utils.Logger) *Server {
	return &Server{
		settings: s,
		log:      log.With("webui"),
		stats:    sysstat.NewCollector("/"),
		sessions: map[string]time.Time{},
	}
}

// Run serves the panel until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	if s.settings.Password == "" {
		return fmt.Errorf("web panel has no login code set — refusing to start unauthenticated")
	}

	// Prime the collector so the first request reports a real CPU figure rather
	// than zero: utilisation is a difference between two samples.
	s.stats.Sample()
	go s.expireSessions(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePanel)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/overview", s.guard(s.handleOverview))
	mux.HandleFunc("/api/logs", s.guard(s.handleLogs))
	mux.HandleFunc("/api/control", s.guard(s.handleControl))

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.settings.Port),
		Handler:           s.securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	s.log.Infof("panel listening on :%d (%s, read-only: %v)",
		s.settings.Port, s.settings.Scheme(), s.settings.ReadOnly)

	var err error
	if s.settings.TLS {
		// A self-signed certificate names the server's own address so a client
		// that verifies can at least match the IP; blank cert/key falls back to
		// self-signed inside tlsutil.
		tlsCfg, cerr := tlsutil.Config(s.settings.TLSCert, s.settings.TLSKey, utils.OutboundIP(), "localhost")
		if cerr != nil {
			return cerr
		}
		srv.TLSConfig = tlsCfg
		// The certificates live in TLSConfig, so the file arguments are empty.
		err = srv.ListenAndServeTLS("", "")
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// securityHeaders applies a strict content policy. The panel is entirely
// self-contained, so it can forbid every external origin outright. Over HTTPS it
// also asks the browser to remember to use TLS.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if s.settings.TLS {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handlePanel(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(panelHTML))
}

// --- authentication ---------------------------------------------------------

type loginRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !constantTimeEqual(req.Password, s.settings.Password) {
		// A uniform small delay blunts trivial online guessing without making a
		// legitimate login feel slow.
		time.Sleep(500 * time.Millisecond)
		http.Error(w, "wrong code", http.StatusUnauthorized)
		return
	}

	token := utils.GenToken(24)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(sessionTTL)
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "backfire_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// Secure only over HTTPS: setting it on a plain-HTTP panel would make the
		// browser drop the cookie and lock the operator out.
		Secure:   s.settings.TLS,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	writeJSON(w, map[string]any{"ok": true})
}

// guard wraps a handler so only a logged-in session reaches it.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("backfire_session")
		if err != nil || !s.validSession(c.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) validSession(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok || time.Now().After(exp) {
		delete(s.sessions, token)
		return false
	}
	return true
}

// expireSessions drops timed-out sessions so the map cannot grow without bound.
func (s *Server) expireSessions(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.mu.Lock()
			for tok, exp := range s.sessions {
				if now.After(exp) {
					delete(s.sessions, tok)
				}
			}
			s.mu.Unlock()
		}
	}
}

// constantTimeEqual compares two secrets without leaking their length
// relationship through timing.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// --- data -------------------------------------------------------------------

type overview struct {
	Host     sysstat.Stats     `json:"host"`
	Uptime   string            `json:"uptime"`
	Version  string            `json:"version"`
	ReadOnly bool              `json:"read_only"`
	Tunnels  []tunnelView      `json:"tunnels"`
	Totals   overviewTotals    `json:"totals"`
	Services map[string]string `json:"services"`
}

type overviewTotals struct {
	RxBytes uint64  `json:"rx_bytes"`
	TxBytes uint64  `json:"tx_bytes"`
	Total   uint64  `json:"total_bytes"`
	RxRate  float64 `json:"rx_rate"`
	TxRate  float64 `json:"tx_rate"`
	Linked  int     `json:"linked"`
	Count   int     `json:"count"`
}

// tunnelView is a tunnel's published state plus what only the panel's host knows
// — whether systemd currently has the unit active.
type tunnelView struct {
	metrics.State
	Status  string `json:"status"`
	Running bool   `json:"running"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	host := s.stats.Sample()
	states := metrics.ReadAll()

	// Every installed tunnel should appear, including one whose engine is
	// stopped and therefore publishing nothing — an absent card is far more
	// confusing than a card that says "inactive".
	byName := make(map[string]metrics.State, len(states))
	for _, st := range states {
		byName[st.Name] = st
	}
	names, _ := manage.List()
	for _, st := range states {
		if !contains(names, st.Name) {
			names = append(names, st.Name)
		}
	}

	views := make([]tunnelView, 0, len(names))
	for _, name := range names {
		st, live := byName[name]
		if !live {
			st = metrics.State{Snapshot: metrics.Snapshot{Name: name, PingMs: -1}}
			if cfg, err := manage.LoadConfig(name); err == nil {
				st.Role = string(cfg.Role)
				if cfg.Role == "server" {
					st.Transport = string(cfg.Server.Transport)
				} else {
					st.Transport = string(cfg.Client.Transport)
				}
			}
		}
		// A fresh state file is proof the engine process is alive, so it outranks
		// systemctl: a tunnel started outside systemd — or a host where the unit
		// state is unreadable — is still demonstrably running.
		status := manage.Status(name)
		running := status == "active" || live
		if live && status != "active" {
			status = "running"
		}
		views = append(views, tunnelView{State: st, Status: status, Running: running})
	}

	rx, tx, rxRate, txRate, linked := metrics.Totals(states)
	writeJSON(w, overview{
		Host:     host,
		Uptime:   sysstat.FormatUptime(host.Uptime),
		Version:  app.Version,
		ReadOnly: s.settings.ReadOnly,
		Tunnels:  views,
		Totals: overviewTotals{
			RxBytes: rx, TxBytes: tx, Total: rx + tx,
			RxRate: rxRate, TxRate: txRate,
			Linked: linked, Count: len(views),
		},
		Services: map[string]string{
			"webui": serviceStatus(app.WebUIService),
			"bot":   serviceStatus(app.BotService),
		},
	})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("tunnel")
	if err := validName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out, err := exec.Command("journalctl", "-u", app.ServiceName(name),
		"-n", "200", "--no-pager", "--output", "short-iso").CombinedOutput()
	if err != nil && len(out) == 0 {
		http.Error(w, "could not read logs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"tunnel": name, "lines": strings.Split(strings.TrimRight(string(out), "\n"), "\n")})
}

type controlRequest struct {
	Tunnel string `json:"tunnel"`
	Action string `json:"action"`
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	if s.settings.ReadOnly {
		http.Error(w, "panel is in monitoring-only mode", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req controlRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := validName(req.Tunnel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch req.Action {
	case "start", "stop", "restart":
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err := manage.Control(req.Action, req.Tunnel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.log.Infof("%s %s via panel", req.Action, req.Tunnel)
	writeJSON(w, map[string]any{"ok": true})
}

// validName rejects anything that could escape into a systemd unit name or a
// shell argument. The name always comes from a client request, so it is never
// trusted.
func validName(name string) error {
	if name == "" {
		return fmt.Errorf("tunnel name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("tunnel name is too long")
	}
	for _, r := range name {
		if !(r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("tunnel name has an invalid character")
		}
	}
	return nil
}

func serviceStatus(unit string) string {
	out, _ := exec.Command("systemctl", "is-active", unit).Output()
	st := strings.TrimSpace(string(out))
	if st == "" {
		return "unknown"
	}
	return st
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
