# backfire 🔥

**backfire** is a high-performance reverse tunnel written in Go, built for the
Iran ⇄ abroad server setup. It ships as a **single binary** with an interactive
CLI menu — you create, start and manage tunnels without ever editing a config
file or writing a systemd unit by hand.

> 📖 راهنمای فارسی: **[README_FA.md](README_FA.md)**

---

## Architecture

An end user connects to a **published port** on the exposed server (the Iran
VPS). The engine carries that connection over **one multiplexed transport link**
to the client abroad, which dials the **real service** and splices the two ends
together.

```
   user ──▶ :443 (exposed server) ══ transport+mux ══▶ client abroad ──▶ 127.0.0.1:443
              [ server role ]                            [ client role ]
```

The tunnel link is always **dialed by the client** (abroad → Iran), so the side
abroad never needs an open inbound port. Every forwarded connection travels as
its own flow-controlled stream over a single physical connection.

---

## Why backfire

- **Nine transports.** Raw TCP, multiplexed TCP, fingerprint-free stealth, UDP,
  UDP+KCP with error correction, and WebSocket over HTTP or TLS in both pooled
  and multiplexed flavours. One keyword switches between them.
- **Stream multiplexing.** All forwarded connections share one link via
  [smux](https://github.com/xtaci/smux), with per-stream flow control — no
  connection storms, no per-connection handshake tax.
- **Warm connection pool.** The non-multiplexed transports keep links pre-dialed
  and pre-authenticated, so no end user ever waits for a dial plus handshake on
  an intercontinental path.
- **Authenticated, replay-resistant handshake.** Peers prove a shared token with
  an HMAC-SHA256 challenge/response; the token never crosses the wire, even on a
  plain `tcp` link. The server answers nothing until a peer proves it speaks the
  protocol, so a scanner learns nothing.
- **Self-healing client.** A dropped link is redialed with exponential backoff
  and full jitter, so the tunnel comes back on its own after any outage.
- **Zero-touch operation.** The menu writes the config, generates the systemd
  unit (`Restart=always`), enables it and starts it. Reboot-safe by default.
- **Small and auditable.** A handful of focused packages, no sprawling
  dependency tree.

---

## Quick start

On the VPS, as root:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/thenawid/backfire/main/install.sh)
```

The installer builds the binary, installs it to `/usr/local/bin/backfire`, and
opens the menu. Reopen it any time with:

```bash
sudo backfire
```

### 1. On the exposed server (Iran)

`backfire` → **Create server tunnel**. Pick a listen port, a transport, and add
your forwards (e.g. `443=127.0.0.1:443`). It prints a **token** — copy it.

### 2. On the origin server (abroad)

`backfire` → **Create client tunnel**. Enter the server address, the same
transport, and paste the token. Done — the client dials in and the tunnel is
live. Both sides survive reboots.

---

## Manual configuration

Each tunnel is one TOML file in `/etc/backfire/` run by a systemd unit. You can
write them by hand too — see [`examples/server.toml`](examples/server.toml) and
[`examples/client.toml`](examples/client.toml).

```bash
backfire -c /etc/backfire/main.toml      # engine mode (what systemd runs)
backfire                                 # interactive menu
backfire -v                              # version
```

### Forwards

Each entry is `<listen>=<target>`:

| Entry | Published on the server | Client dials |
|---|---|---|
| `443=127.0.0.1:443` | `0.0.0.0:443` | `127.0.0.1:443` |
| `0.0.0.0:2222=127.0.0.1:22` | `0.0.0.0:2222` | `127.0.0.1:22` |
| `8080` | `0.0.0.0:8080` | `127.0.0.1:8080` |

---

## Transports

Nine names, but only two dimensions. Every transport is a **base** (how one raw
stream is obtained) combined with a **mode** (how forwarded connections share
those streams) — which is why nine options need only five stream providers.

| Transport | Base | Mode | Encrypted | Use when |
|---|---|---|---|---|
| `tcp` | TCP | pool | no | clean path, lowest possible overhead |
| `tcpmux` | TCP | mux | no | many concurrent connections, fewest sockets |
| `stealth` | TCP | mux | **yes** (AES-256-GCM) | DPI is fingerprinting your traffic |
| `udp` | UDP | mux | no | TCP is throttled or blocked on the path |
| `kcp` | UDP | mux | **yes** (AES-256) | lossy or actively degraded path |
| `ws` | WebSocket | pool | no | a CDN or L7 proxy sits in front |
| `wsmux` | WebSocket | mux | no | as `ws`, with many connections |
| `wss` | WebSocket/TLS | pool | **yes** (TLS) | encryption plus CDN compatibility |
| `wssmux` | WebSocket/TLS | mux | **yes** (TLS) | as `wss`, with many connections |

### mux vs pool

**`mux`** multiplexes every forwarded connection onto a single physical link as
its own flow-controlled stream. One socket, no per-connection setup cost. The
trade-off is that all streams share one link's fate and one congestion window.

**`pool`** gives each forwarded connection its own link, drawn from a set the
client keeps pre-dialed and already past the token handshake. Nothing waits for a
round trip on the critical path, and one stalled connection cannot affect
another. Costs more sockets. Tune with `[server.pool] size`.

### Notes on specific transports

**`stealth`** wraps TCP in an encrypted record layer keyed from the tunnel token.
Unlike TLS there is no handshake to fingerprint, no certificate, no version
negotiation, and no fixed header — the only plaintext is a 32-byte random salt
per side, after which every record is indistinguishable from random bytes. Deep
packet inspection has nothing to match on.

**`udp` vs `kcp`** — a tunnel link must be a reliable, ordered stream to carry the
handshake and multiplexer, which raw UDP is not. Both transports use the KCP
protocol over UDP datagrams to supply that. `udp` runs it bare, the lightest way
to get a reliable stream over UDP. `kcp` adds Reed-Solomon **forward error
correction** (10 data : 3 parity by default, so moderate loss is repaired without
waiting for a retransmit) and **AES-256** keyed from the token. Both ends must
agree on `mtu` and the shard ratio. They forward TCP services; forwarding a UDP
service is on the roadmap.

**`wss` / `wssmux`** — leaving `tls_cert`/`tls_key` empty generates an in-memory
self-signed certificate. That's fine, because peers authenticate by the **token**,
not the certificate chain. Point them at a real certificate if you terminate a
named domain, and set `tls_verify = true` on the client.

---

## Security notes

- The token is the whole security boundary — generate a long random one
  (menu option 7 or `backfire` prints one) and keep the config files `0600`
  (the installer does this for you).
- On the client, set `allowed_targets` to pin exactly which `host:port` the
  server may ask it to dial, if you don't want the server choosing freely.
- Config files hold live tokens — they are ignored by `.gitignore` patterns and
  should never be committed.

---

## Build & test from source

```bash
git clone https://github.com/thenawid/backfire.git
cd backfire
make build      # -> ./backfire
make test       # unit tests + in-process end-to-end tunnel over tcp/ws/wss
make vet
```

The end-to-end test in [`internal/e2e`](internal/e2e) stands up a real
server+client over the loopback for every transport and asserts a byte
round-trip through the full stack (handshake, mux, framing, forwarding).

---

## Project layout

```
main.go                 engine/menu dispatch
config/                 TOML schema, transport table, validation
cmd/                    default configs for the menu
internal/
  app/                  version, paths, engine dispatch
  protocol/             token handshake + stream target framing
  transport/            stream providers: tcp, stealth, udp/kcp, ws, wss
  mux/                  smux wrapper (mux mode)
  pool/                 warm pre-authenticated link pool (pool mode)
  server/               exposed side: publish ports, hand off connections
  client/               origin side: link, reconnect, serve targets
  manage/               systemd units + tunnel lifecycle
  menu/                 interactive CLI
  utils/                logger, token, pipe helpers
  e2e/                  end-to-end tunnel test across all transports
```

---

## Roadmap

- Forwarding UDP *services* (the udp/kcp transports carry TCP services today)
- A QUIC transport
- Optional web panel and Telegram status reporting
- Per-tunnel metrics and a health watchdog
- Prebuilt release binaries + checksum-verified installs

---

## Acknowledgements

backfire is an independent, from-scratch implementation. Its overall shape was
inspired by [BackPack](https://github.com/AminMGMT/BackPack); **no code was
copied** from it. See [NOTICE](NOTICE).

## License

MIT — see [LICENSE](LICENSE).
