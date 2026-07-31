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

// Transport names the wire protocol carrying a tunnel between two peers.
//
// Every transport decomposes into two independent choices, which is what keeps
// nine names from turning into nine implementations:
//
//   - a Base — how a single raw byte stream is obtained (tcp, udp, kcp, ws, wss)
//   - a Mode — how many end-user connections share one such stream:
//     ModeMux multiplexes them all onto one link, while ModePool keeps a warm
//     pool of links and hands each connection its own.
//
// The auth handshake and the forwarding logic sit above both and are identical
// for all nine.
type Transport string

const (
	// TCP is a raw TCP stream, one pooled link per forwarded connection —
	// lowest possible overhead, most conspicuous to DPI.
	TCP Transport = "tcp"
	// TCPMUX multiplexes every forwarded connection onto one TCP link. Fewer
	// sockets and no per-connection setup cost, at the price of head-of-line
	// coupling on a lossy path.
	TCPMUX Transport = "tcpmux"
	// STEALTH is a TCP link wrapped in an encrypted record layer keyed by the
	// tunnel token. It has no TLS fingerprint and no recognisable handshake —
	// on the wire it is indistinguishable from random bytes, so DPI has no
	// pattern to match. Multiplexed.
	STEALTH Transport = "stealth"
	// UDP carries the tunnel inside UDP datagrams with a minimal reliable,
	// ordered stream layer on top. For paths where TCP is throttled or blocked.
	UDP Transport = "udp"
	// KCP is the UDP transport with the full KCP protocol: forward error
	// correction repairs loss without waiting for a retransmit, and the stream
	// is encrypted with AES-256 keyed by the token. The best choice on a lossy
	// or actively-degraded path.
	KCP Transport = "kcp"
	// WS is a WebSocket link over plain HTTP — survives CDNs and L7 proxies.
	// Pooled.
	WS Transport = "ws"
	// WSMUX is the WebSocket link, multiplexed.
	WSMUX Transport = "wsmux"
	// WSS is a WebSocket link over TLS — CDN friendly and encrypted. Pooled.
	WSS Transport = "wss"
	// WSSMUX is the TLS WebSocket link, multiplexed.
	WSSMUX Transport = "wssmux"
)

// KnownTransports lists every transport the build understands, in the order the
// interactive menu presents them.
var KnownTransports = []Transport{
	TCP, TCPMUX, STEALTH, UDP, KCP, WS, WSMUX, WSS, WSSMUX,
}

// Base is the underlying stream provider of a transport.
type Base string

const (
	BaseTCP     Base = "tcp"
	BaseStealth Base = "stealth"
	BaseUDP     Base = "udp"
	BaseKCP     Base = "kcp"
	BaseWS      Base = "ws"
	BaseWSS     Base = "wss"
)

// Mode is how forwarded connections share transport links.
type Mode string

const (
	// ModeMux multiplexes every forwarded connection onto a single link.
	ModeMux Mode = "mux"
	// ModePool gives each forwarded connection its own link, drawn from a warm
	// pool of pre-dialed spares so no connection pays for a fresh handshake.
	ModePool Mode = "pool"
)

// Split returns the base stream provider and the sharing mode of a transport.
func (t Transport) Split() (Base, Mode) {
	switch t {
	case TCP:
		return BaseTCP, ModePool
	case TCPMUX:
		return BaseTCP, ModeMux
	case STEALTH:
		return BaseStealth, ModeMux
	case UDP:
		return BaseUDP, ModeMux
	case KCP:
		return BaseKCP, ModeMux
	case WS:
		return BaseWS, ModePool
	case WSMUX:
		return BaseWS, ModeMux
	case WSS:
		return BaseWSS, ModePool
	case WSSMUX:
		return BaseWSS, ModeMux
	default:
		return "", ""
	}
}

// Base returns just the stream provider of a transport.
func (t Transport) Base() Base { b, _ := t.Split(); return b }

// Mode returns just the sharing mode of a transport.
func (t Transport) Mode() Mode { _, m := t.Split(); return m }

// IsMux reports whether a transport multiplexes onto a single link.
func (t Transport) IsMux() bool { return t.Mode() == ModeMux }

// Describe returns a one-line human summary, used by the menu.
func (t Transport) Describe() string {
	switch t {
	case TCP:
		return "raw TCP, pooled links — lowest overhead"
	case TCPMUX:
		return "raw TCP, multiplexed — fewest sockets"
	case STEALTH:
		return "encrypted TCP with no fingerprint — DPI sees random bytes"
	case UDP:
		return "reliable stream over UDP datagrams"
	case KCP:
		return "UDP + KCP: error correction and AES-256 — best on lossy paths"
	case WS:
		return "WebSocket over HTTP, pooled — passes CDNs and L7 proxies"
	case WSMUX:
		return "WebSocket over HTTP, multiplexed"
	case WSS:
		return "WebSocket over TLS, pooled — encrypted and CDN friendly"
	case WSSMUX:
		return "WebSocket over TLS, multiplexed"
	default:
		return "unknown"
	}
}

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

// KCPConfig tunes the KCP transport: a reliable, retransmitting protocol
// carried inside UDP datagrams. Defaults target a lossy intercontinental path
// rather than a clean LAN, because that is the case KCP exists for.
type KCPConfig struct {
	// MTU bounds a single datagram; stay under the path MTU to avoid IP
	// fragmentation, which multiplies effective loss.
	MTU int `toml:"mtu"`
	// Interval is the protocol tick in milliseconds. Lower reacts faster and
	// costs more CPU.
	Interval int `toml:"interval"`
	// Resend is the number of duplicate ACKs that trigger a fast retransmit.
	Resend int `toml:"resend"`
	// NoDelay enables the low-latency profile (1) instead of the default (0).
	NoDelay int `toml:"nodelay"`
	// NoCongestion disables the congestion window (1) so a policed path cannot
	// throttle the tunnel by inducing loss.
	NoCongestion int `toml:"nocongestion"`
	// SndWnd / RcvWnd are the send and receive windows in packets.
	SndWnd int `toml:"sndwnd"`
	RcvWnd int `toml:"rcvwnd"`
	// DataShards / ParityShards enable forward error correction: for every
	// DataShards packets, ParityShards extra ones are sent so that moderate
	// loss is repaired instantly instead of waiting for a retransmit.
	// Both zero disables FEC.
	DataShards   int `toml:"datashards"`
	ParityShards int `toml:"parityshards"`
	// SmuxBuf / StreamBuf size the socket buffers in bytes.
	SocketBuf int `toml:"socket_buf"`
}

func (k KCPConfig) withDefaults() KCPConfig {
	if k.MTU <= 0 {
		k.MTU = 1350
	}
	if k.Interval <= 0 {
		k.Interval = 20
	}
	if k.Resend < 0 {
		k.Resend = 2
	}
	if k.SndWnd <= 0 {
		k.SndWnd = 1024
	}
	if k.RcvWnd <= 0 {
		k.RcvWnd = 1024
	}
	if k.SocketBuf <= 0 {
		k.SocketBuf = 4 * 1024 * 1024
	}
	// Parity without data shards is meaningless to the encoder, so treat a
	// half-configured pair as FEC disabled rather than failing to start.
	if k.DataShards <= 0 || k.ParityShards <= 0 {
		k.DataShards, k.ParityShards = 0, 0
	}
	return k
}

// PoolConfig tunes the warm connection pool used by the non-multiplexed
// transports (tcp, ws, wss).
//
// The client keeps Size links pre-dialed and already past the token handshake,
// parked and waiting. When the server needs to forward a connection it grabs a
// ready link instead of paying for a dial plus handshake on the critical path,
// and the client immediately dials a replacement to refill the pool.
type PoolConfig struct {
	// Size is how many spare links to keep warm. 0 falls back to the default.
	Size int `toml:"size"`
	// IdleTimeout is how long, in seconds, an unused parked link is kept before
	// being recycled. Keeps a stale NAT mapping from being handed out.
	IdleTimeout int `toml:"idle_timeout"`
}

func (p PoolConfig) withDefaults() PoolConfig {
	if p.Size <= 0 {
		p.Size = 8
	}
	if p.Size > 512 {
		p.Size = 512
	}
	if p.IdleTimeout <= 0 {
		p.IdleTimeout = 120
	}
	return p
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
	KeepAlive int        `toml:"keepalive"`
	Mux       MuxConfig  `toml:"mux"`
	KCP       KCPConfig  `toml:"kcp"`
	Pool      PoolConfig `toml:"pool"`
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
	KCP       KCPConfig       `toml:"kcp"`
	Pool      PoolConfig      `toml:"pool"`
}

// LogConfig controls the process logger.
type LogConfig struct {
	// Level is one of debug, info, warn, error.
	Level string `toml:"level"`
}

// Config is a whole tunnel file. Exactly one of Server / Client is used,
// selected by Role.
type Config struct {
	// Family selects which kind of tunnel this file describes. It defaults to
	// the BackPack family so every existing config keeps working unchanged.
	Family   Family         `toml:"family"`
	Role     Role           `toml:"role"`
	Server   ServerConfig   `toml:"server"`
	Client   ClientConfig   `toml:"client"`
	Backhaul BackhaulConfig `toml:"backhaul"`
	Log      LogConfig      `toml:"log"`
}

// Family is the class of tunnel a config describes.
type Family string

const (
	// FamilyBackpack is the userspace reverse tunnel that forwards TCP
	// connections over one of the nine transports — the default.
	FamilyBackpack Family = "backpack"
	// FamilyBackhaul is a layer-3 point-to-point tunnel over a TUN device,
	// carried inside another IP protocol (icmp, gre, ipip, …) with optional
	// source-address spoofing, for paths where even the BackPack transports are
	// filtered.
	FamilyBackhaul Family = "backhaul"
)

// Carrier is the protocol a Backhaul tunnel hides its encrypted layer-3 frames
// inside. The point is camouflage: to a filter, an icmp carrier looks like ping
// traffic and a gre carrier looks like an ordinary GRE tunnel.
type Carrier string

const (
	CarrierUDP  Carrier = "udp"  // UDP datagrams on a port
	CarrierTCP  Carrier = "tcp"  // a single framed TCP stream
	CarrierICMP Carrier = "icmp" // raw ICMP echo request/reply
	CarrierIPIP Carrier = "ipip" // raw IP, protocol 4 (looks like IP-in-IP)
	CarrierGRE  Carrier = "gre"  // raw IP, protocol 47 (looks like GRE)
	CarrierVRRP Carrier = "vrrp" // raw IP, protocol 112 (looks like VRRP)
	CarrierBIP  Carrier = "bip"  // raw IP, experimental protocol 253
)

// KnownCarriers lists every Backhaul carrier, in menu order.
var KnownCarriers = []Carrier{
	CarrierUDP, CarrierICMP, CarrierIPIP, CarrierGRE, CarrierVRRP, CarrierTCP, CarrierBIP,
}

// NeedsPort reports whether a carrier binds a transport port (udp/tcp) rather
// than riding a raw IP protocol.
func (c Carrier) NeedsPort() bool { return c == CarrierUDP || c == CarrierTCP }

// IsRaw reports whether a carrier uses a raw IP socket keyed by protocol number.
func (c Carrier) IsRaw() bool {
	switch c {
	case CarrierICMP, CarrierIPIP, CarrierGRE, CarrierVRRP, CarrierBIP:
		return true
	default:
		return false
	}
}

// Protocol returns the IP protocol number a raw carrier uses, or 0 for the
// port-based carriers.
func (c Carrier) Protocol() int {
	switch c {
	case CarrierICMP:
		return 1
	case CarrierIPIP:
		return 4
	case CarrierGRE:
		return 47
	case CarrierVRRP:
		return 112
	case CarrierBIP:
		return 253 // RFC 3692 experimentation range
	default:
		return 0
	}
}

// Describe returns a one-line human summary of a carrier for the menu.
func (c Carrier) Describe() string {
	switch c {
	case CarrierUDP:
		return "UDP datagrams — simple and fast"
	case CarrierICMP:
		return "raw ICMP echo — looks like ping traffic"
	case CarrierIPIP:
		return "raw IP proto 4 — looks like IP-in-IP"
	case CarrierGRE:
		return "raw IP proto 47 — looks like a GRE tunnel"
	case CarrierVRRP:
		return "raw IP proto 112 — looks like VRRP"
	case CarrierTCP:
		return "a single framed TCP stream"
	case CarrierBIP:
		return "raw IP proto 253 — experimental camouflage"
	default:
		return "unknown"
	}
}

func validCarrier(c Carrier) bool {
	for _, k := range KnownCarriers {
		if k == c {
			return true
		}
	}
	return false
}

// BackhaulConfig is a layer-3 tunnel. Both peers share a token; the TUN device
// on each side is given LocalIP, and traffic to RemoteIP is carried encrypted
// over the chosen carrier to the peer.
type BackhaulConfig struct {
	// Carrier is the protocol the encrypted frames hide inside.
	Carrier Carrier `toml:"carrier"`
	// Peer is the other server's public address. The client dials it; the
	// server may leave it blank to learn the peer from the first packet.
	Peer string `toml:"peer"`
	// Port is the carrier port for the udp/tcp carriers.
	Port int `toml:"port"`
	// Token is the shared secret; the AES-256 frame key is derived from it.
	Token string `toml:"token"`
	// LocalIP / RemoteIP are the TUN interface addresses on each end.
	LocalIP  string `toml:"local_ip"`
	RemoteIP string `toml:"remote_ip"`
	// MTU of the TUN device.
	MTU int `toml:"mtu"`
	// IFName is the TUN interface name (auto-generated when blank).
	IFName string `toml:"ifname"`
	// Spoof, when set, sends carrier packets with a forged source address so the
	// real origin is hidden. Requires a raw carrier.
	Spoof bool `toml:"spoof"`
	// SpoofSource is the forged source address used when Spoof is on.
	SpoofSource string `toml:"spoof_source"`
	// Forwards optionally publishes ports over the established tunnel, each
	// "<listen>=<target>" like a BackPack server, with the target reached across
	// the layer-3 link.
	Forwards []string `toml:"forwards"`
	// Raw is an advanced escape hatch: extra key=value lines appended verbatim,
	// for options the menu does not expose. It is never interpreted by backfire
	// itself, only stored and shown.
	Raw string `toml:"raw"`
}

func (b BackhaulConfig) withDefaults() BackhaulConfig {
	if b.MTU <= 0 {
		b.MTU = 1400
	}
	if b.Carrier == "" {
		b.Carrier = CarrierICMP
	}
	return b
}

func (b BackhaulConfig) validate() error {
	if !validCarrier(b.Carrier) {
		return fmt.Errorf("backhaul.carrier %q is unknown", b.Carrier)
	}
	if b.Token == "" {
		return fmt.Errorf("backhaul.token is required")
	}
	if b.LocalIP == "" || net.ParseIP(b.LocalIP) == nil {
		return fmt.Errorf("backhaul.local_ip %q is not a valid IP", b.LocalIP)
	}
	if b.RemoteIP == "" || net.ParseIP(b.RemoteIP) == nil {
		return fmt.Errorf("backhaul.remote_ip %q is not a valid IP", b.RemoteIP)
	}
	if b.Peer != "" && net.ParseIP(b.Peer) == nil {
		return fmt.Errorf("backhaul.peer %q is not a valid IP", b.Peer)
	}
	if b.Carrier.NeedsPort() {
		if b.Port <= 0 || b.Port > 65535 {
			return fmt.Errorf("backhaul.port %d is out of range for the %s carrier", b.Port, b.Carrier)
		}
	}
	if b.Spoof {
		if !b.Carrier.IsRaw() {
			return fmt.Errorf("spoofing needs a raw carrier (icmp/ipip/gre/vrrp/bip), not %s", b.Carrier)
		}
		if b.SpoofSource != "" && net.ParseIP(b.SpoofSource) == nil {
			return fmt.Errorf("backhaul.spoof_source %q is not a valid IP", b.SpoofSource)
		}
	}
	for _, raw := range b.Forwards {
		if _, err := ParseForward(raw); err != nil {
			return err
		}
	}
	return nil
}

// ParsedForwards returns the parsed forward table for a backhaul config.
func (b BackhaulConfig) ParsedForwards() ([]Forward, error) {
	out := make([]Forward, 0, len(b.Forwards))
	for _, raw := range b.Forwards {
		f, err := ParseForward(raw)
		if err != nil {
			return nil, err
		}
		// In backhaul the target is reached across the layer-3 link, so a bare
		// port means "the same port on the peer's tunnel IP".
		if f.Target == net.JoinHostPort("127.0.0.1", portOf(f.Listen)) {
			f.Target = net.JoinHostPort(b.RemoteIP, portOf(f.Listen))
		}
		out = append(out, f)
	}
	return out, nil
}

func portOf(hostport string) string {
	_, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return ""
	}
	return p
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
	if c.Family == "" {
		c.Family = FamilyBackpack
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	c.Backhaul = c.Backhaul.withDefaults()
	c.Server.Mux = c.Server.Mux.withDefaults()
	c.Client.Mux = c.Client.Mux.withDefaults()
	c.Server.KCP = c.Server.KCP.withDefaults()
	c.Client.KCP = c.Client.KCP.withDefaults()
	c.Server.Pool = c.Server.Pool.withDefaults()
	c.Client.Pool = c.Client.Pool.withDefaults()
	c.Client.Reconnect = c.Client.Reconnect.withDefaults()
	if c.Server.KeepAlive <= 0 {
		c.Server.KeepAlive = 30
	}
	if c.Client.KeepAlive <= 0 {
		c.Client.KeepAlive = 30
	}
}

// Validate checks that the active family and role are internally consistent.
func (c *Config) Validate() error {
	if c.Family == FamilyBackhaul {
		return c.Backhaul.validate()
	}
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
