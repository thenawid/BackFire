// Package config defines the on-disk (TOML) configuration for a single
// backfire tunnel and the helpers to load, validate and persist it.
//
// A tunnel file describes exactly one role — a "server" (the exposed side,
// typically the Iran VPS) or a "client" (the origin side, typically abroad).
// Only the section matching the top-level `role` key is consulted; the other
// section may be omitted entirely.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Role selects which half of a tunnel a config describes.
type Role string

const (
	RoleServer Role = "server"
	RoleClient Role = "client"
)

// Transport is the wire protocol carrying the multiplexed tunnel between the
// two peers. The set is deliberately small but each entry is a full net.Conn
// provider, so new transports slot in without touching the mux or protocol
// layers above them.
type Transport string

const (
	// TCP is a raw TCP stream — lowest overhead, most conspicuous to DPI.
	TCP Transport = "tcp"
	// WS is a WebSocket stream over plain HTTP — survives CDNs and L7 proxies.
	WS Transport = "ws"
	// WSS is a WebSocket stream over TLS — CDN friendly and encrypted.
	WSS Transport = "wss"
)

// KnownTransports lists every transport the build understands, for validation
// and for the interactive menu.
var KnownTransports = []Transport{TCP, WS, WSS}

// MuxConfig tunes the stream multiplexer that carries every forwarded
// connection over the single physical transport link.
type MuxConfig struct {
	// Version is the smux protocol version (1 or 2). 2 supports per-stream
	// flow control windows and is the sensible default.
	Version int `toml:"version"`
	// KeepAlive is the interval, in seconds, between mux keepalive frames.
	KeepAlive int `toml:"keepalive"`
	// MaxFrameSize bounds a single mux frame in bytes.
	MaxFrameSize int `toml:"max_frame_size"`
	// MaxReceiveBuffer is the whole-session receive window in bytes.
	MaxReceiveBuffer int `toml:"max_receive_buffer"`
	// MaxStreamBuffer is the per-stream receive window in bytes.
	MaxStreamBuffer int `toml:"max_stream_buffer"`
}

func (m MuxConfig) withDefaults() MuxConfig {
	if m.Version != 1 && m.Version != 2 {
		m.Version = 2
	}
	if m.KeepAlive <= 0 {
		m.KeepAlive = 10
	}
	if m.MaxFrameSize <= 0 {
		m.MaxFrameSize = 32 * 1024
	}
	if m.MaxReceiveBuffer <= 0 {
		m.MaxReceiveBuffer = 4 * 1024 * 1024
	}
	if m.MaxStreamBuffer <= 0 {
		m.MaxStreamBuffer = 256 * 1024
	}
	return m
}

// ReconnectConfig controls the client's exponential-backoff redial loop.
type ReconnectConfig struct {
	// MinBackoff is the first wait, in seconds, after a link drops.
	MinBackoff int `toml:"min_backoff"`
	// MaxBackoff caps the wait, in seconds, between attempts.
	MaxBackoff int `toml:"max_backoff"`
}

func (r ReconnectConfig) withDefaults() ReconnectConfig {
	if r.MinBackoff <= 0 {
		r.MinBackoff = 1
	}
	if r.MaxBackoff <= 0 {
		r.MaxBackoff = 30
	}
	if r.MaxBackoff < r.MinBackoff {
		r.MaxBackoff = r.MinBackoff
	}
	return r
}

// ServerConfig is the exposed side of a tunnel. It listens for the client to
// dial in, then publishes the forwarded ports to the outside world.
type ServerConfig struct {
	// Bind is the address the tunnel listener binds, e.g. "0.0.0.0:6060".
	Bind string `toml:"bind"`
	// Transport is the wire protocol for the tunnel link.
	Transport Transport `toml:"transport"`
	// Token is the shared secret both peers authenticate with.
	Token string `toml:"token"`
	// Forwards maps a public listener to the target the client must dial,
	// each entry written as "<listen>=<target>", e.g. "80=127.0.0.1:80" or
	// "0.0.0.0:2222=127.0.0.1:22". A bare "<listen>" reuses the same host:port
	// on the client side.
	Forwards []string `toml:"forwards"`
	// TLSCert / TLSKey point at a certificate for the wss transport. When both
	// are empty a self-signed certificate is generated in memory at start-up.
	TLSCert string `toml:"tls_cert"`
	TLSKey  string `toml:"tls_key"`
	// KeepAlive is the TCP keepalive period in seconds for the physical link.
	KeepAlive int       `toml:"keepalive"`
	Mux       MuxConfig `toml:"mux"`
}

// ClientConfig is the origin side of a tunnel. It dials the server, proves the
// token, then serves whatever streams the server opens by connecting to the
// requested local targets.
type ClientConfig struct {
	// Server is the address of the tunnel listener, e.g. "1.2.3.4:6060".
	Server string `toml:"server"`
	// Transport must match the server's transport.
	Transport Transport `toml:"transport"`
	// Token is the shared secret.
	Token string `toml:"token"`
	// TLSVerify enables certificate verification for the wss transport. Leave
	// off when the server uses a self-signed certificate.
	TLSVerify bool `toml:"tls_verify"`
	// ServerName overrides the SNI / certificate name for wss (useful behind a
	// CDN). Empty derives it from the server address.
	ServerName string `toml:"server_name"`
	// AllowedTargets, when non-empty, restricts which "host:port" the server is
	// permitted to ask this client to dial. Empty means allow any.
	AllowedTargets []string `toml:"allowed_targets"`
	// KeepAlive is the TCP keepalive period in seconds for the physical link.
	KeepAlive int             `toml:"keepalive"`
	Reconnect ReconnectConfig `toml:"reconnect"`
	Mux       MuxConfig       `toml:"mux"`
}

// LogConfig controls the process logger.
type LogConfig struct {
	// Level is one of debug, info, warn, error.
	Level string `toml:"level"`
}

// Config is a whole tunnel file. Exactly one of Server / Client is used,
// selected by Role.
type Config struct {
	Role   Role         `toml:"role"`
	Server ServerConfig `toml:"server"`
	Client ClientConfig `toml:"client"`
	Log    LogConfig    `toml:"log"`
}

// Forward is a parsed [ServerConfig.Forwards] entry.
type Forward struct {
	// Listen is the local address the server publishes, e.g. "0.0.0.0:80".
	Listen string
	// Target is the address the client dials on its side, e.g. "127.0.0.1:80".
	Target string
}

// Load reads and validates a tunnel config from disk, filling in defaults.
func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes a config to disk as TOML.
func Save(path string, c *Config) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

func (c *Config) applyDefaults() {
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	c.Server.Mux = c.Server.Mux.withDefaults()
	c.Client.Mux = c.Client.Mux.withDefaults()
	c.Client.Reconnect = c.Client.Reconnect.withDefaults()
	if c.Server.KeepAlive <= 0 {
		c.Server.KeepAlive = 30
	}
	if c.Client.KeepAlive <= 0 {
		c.Client.KeepAlive = 30
	}
}

// Validate checks that the active role is internally consistent.
func (c *Config) Validate() error {
	switch c.Role {
	case RoleServer:
		return c.Server.validate()
	case RoleClient:
		return c.Client.validate()
	default:
		return fmt.Errorf("role must be %q or %q, got %q", RoleServer, RoleClient, c.Role)
	}
}

func validTransport(t Transport) bool {
	for _, k := range KnownTransports {
		if k == t {
			return true
		}
	}
	return false
}

func (s ServerConfig) validate() error {
	if s.Bind == "" {
		return fmt.Errorf("server.bind is required")
	}
	if _, _, err := net.SplitHostPort(s.Bind); err != nil {
		return fmt.Errorf("server.bind %q is not host:port: %w", s.Bind, err)
	}
	if !validTransport(s.Transport) {
		return fmt.Errorf("server.transport %q is unknown", s.Transport)
	}
	if s.Token == "" {
		return fmt.Errorf("server.token is required")
	}
	if len(s.Forwards) == 0 {
		return fmt.Errorf("server.forwards is empty — nothing to publish")
	}
	for _, raw := range s.Forwards {
		if _, err := ParseForward(raw); err != nil {
			return err
		}
	}
	return nil
}

func (c ClientConfig) validate() error {
	if c.Server == "" {
		return fmt.Errorf("client.server is required")
	}
	if _, _, err := net.SplitHostPort(c.Server); err != nil {
		return fmt.Errorf("client.server %q is not host:port: %w", c.Server, err)
	}
	if !validTransport(c.Transport) {
		return fmt.Errorf("client.transport %q is unknown", c.Transport)
	}
	if c.Token == "" {
		return fmt.Errorf("client.token is required")
	}
	return nil
}

// Forwards returns the parsed forward table for a server config.
func (s ServerConfig) ParsedForwards() ([]Forward, error) {
	out := make([]Forward, 0, len(s.Forwards))
	for _, raw := range s.Forwards {
		f, err := ParseForward(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// ParseForward turns a "<listen>=<target>" (or bare "<listen>") entry into a
// Forward. A bare port such as "80" binds "0.0.0.0:80" and dials the same port
// on the client's loopback.
func ParseForward(raw string) (Forward, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Forward{}, fmt.Errorf("empty forward entry")
	}
	listenPart, targetPart, hasTarget := strings.Cut(raw, "=")
	listenPart = strings.TrimSpace(listenPart)
	targetPart = strings.TrimSpace(targetPart)

	listen, err := normalizeListen(listenPart)
	if err != nil {
		return Forward{}, fmt.Errorf("forward %q: %w", raw, err)
	}
	if !hasTarget || targetPart == "" {
		// No explicit target: reuse the published port on the client loopback.
		_, port, _ := net.SplitHostPort(listen)
		return Forward{Listen: listen, Target: net.JoinHostPort("127.0.0.1", port)}, nil
	}
	target, err := normalizeTarget(targetPart)
	if err != nil {
		return Forward{}, fmt.Errorf("forward %q: %w", raw, err)
	}
	return Forward{Listen: listen, Target: target}, nil
}

func normalizeListen(s string) (string, error) {
	if _, err := strconv.Atoi(s); err == nil {
		return net.JoinHostPort("0.0.0.0", s), nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("listen %q must be a port or host:port", s)
	}
	if host == "" {
		host = "0.0.0.0"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("listen port %q is not a number", port)
	}
	return net.JoinHostPort(host, port), nil
}

func normalizeTarget(s string) (string, error) {
	if _, err := strconv.Atoi(s); err == nil {
		return net.JoinHostPort("127.0.0.1", s), nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("target %q must be a port or host:port", s)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("target port %q is not a number", port)
	}
	return net.JoinHostPort(host, port), nil
}
