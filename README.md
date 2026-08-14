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
- **Two tunnel modes.** A transport mode that forwards TCP over nine transports,
  and a layer-3 mode that runs a TUN tunnel disguised as ICMP/GRE/IPIP/VRRP with
  optional source spoofing, for the hardest-filtered paths.
- **Web panel and Telegram bot.** A self-contained browser dashboard with live
  gauges and per-tunnel throughput charts, and a bot that reports status, sends
  backups and alerts you when something crosses a threshold.
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

The installer downloads the prebuilt binary for your architecture from the
latest release, verifies it against the published SHA-256 checksum, installs it
to `/usr/local/bin/backfire` and opens the menu — no Go toolchain, no compiling.
(If no prebuilt binary is available it falls back to building from source.)
Reopen the menu any time with:

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
backfire -c /etc/backfire/main.toml      # engine mode (what a tunnel unit runs)
backfire -webui                          # web panel (what backfire-webui runs)
backfire -bot                            # telegram bot (what backfire-bot runs)
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

## Layer-3 tunnels

Alongside the nine transports (which forward TCP connections), backfire has a
second mode: a layer-3 point-to-point tunnel over a TUN device. Where the
transports disguise *how* a connection is carried, this mode disguises the
traffic as *another IP protocol entirely* and can forge its source address — for
paths where even the transports are filtered.

Each end gets a TUN interface and a private point-to-point address; every IP
packet is encrypted with AES-256-GCM (key derived from the shared token) and
sent inside the chosen carrier:

| Carrier | Looks like | Port | Spoof |
|---|---|---|---|
| `udp` | UDP traffic | yes | no |
| `tcp` | a TCP stream | yes | no |
| `icmp` | ping (ICMP echo) | no | yes |
| `ipip` | IP-in-IP (proto 4) | no | yes |
| `gre` | a GRE tunnel (proto 47) | no | yes |
| `vrrp` | VRRP (proto 112) | no | yes |
| `bip` | experimental (proto 253) | no | yes |

The **ICMP carrier** rides real ping traffic the way it actually flows: the
client sends echo *requests* and the server answers with echo *replies*, so the
frames pass NAT and stateful firewalls that would drop a bare, unsolicited
reply. While an ICMP tunnel is up the server sets `net.ipv4.icmp_echo_ignore_all`
so its kernel does not also auto-answer those requests (which would loop the
client's own frames back); the value is restored when the tunnel stops. If ICMP
never links, the provider is likely filtering it — switch both ends to the
`udp` or `tcp` carrier.

**IP spoofing** (the raw carriers) sends carrier packets with a forged source
address, so the real origin never appears on the wire. Leave the source blank
for a random one that changes each restart.

This mode needs **root** (CAP_NET_ADMIN for the TUN device, CAP_NET_RAW for the
raw carriers). The interface is created and addressed through ioctls directly,
with no dependency on `iproute2`. See
[`examples/backhaul.toml`](examples/backhaul.toml); create one interactively with
`sudo backfire` → *Create tunnel* → **layer-3 tunnel**.

> The raw carriers reuse a protocol's *number* for camouflage; they do not
> emit that protocol's full header format. A filter classifying by protocol
> number sees `gre`/`vrrp`/etc.; one parsing the protocol's headers would not.
> Forwarding a UDP *service* over the tunnel is still on the roadmap — today the
> layer-3 tunnel carries whatever the OS routes over the interface, and the
> optional `forwards` publish TCP ports across it.

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

## Web panel

`sudo backfire` → **Web panel** → *Set up*. It asks for a port, suggests a login
code (press Enter to accept it) and asks whether the panel should be
monitoring-only. Then it writes the unit and starts it.

The panel shows host CPU / memory / disk / swap, uplink throughput, and a card
per tunnel with its transport, ports, ping, live traffic, a throughput sparkline
and its journal logs. It refreshes every three seconds.

It is one embedded HTML file with **no external assets** — no CDN, no fonts, no
scripts to fetch — so it works on a server with no outbound internet access and
under a strict content-security policy.

**HTTPS.** Answer yes to *Serve over HTTPS* and the panel serves TLS. Leave the
certificate path blank for a self-signed certificate generated in memory (your
browser warns once), or point it at a real cert/key pair. Over HTTPS the session
cookie is marked `Secure` and an HSTS header is sent.

> The login code is the only thing protecting the panel. Use a long one, turn on
> HTTPS, and prefer reaching it over an SSH tunnel or a VPN rather than opening
> its port to the world.

## Telegram bot

`sudo backfire` → **Telegram bot** → *Set up*. Paste the token from
[@BotFather](https://t.me/BotFather) and your numeric ID from
[@userinfobot](https://t.me/userinfobot).

| Command | What it does |
|---|---|
| `/status` | every tunnel: state, ports, traffic, ping |
| `/system` | processor, memory, disk, uptime |
| `/backup` | sends every config and setting here as a `.tar.gz` |
| `/update` | check for and install a new release without dropping tunnels |
| `/alerts` | current alert thresholds |
| `/webui` | panel link and login code |
| `/support` | project links |

Every reply carries an inline keyboard, and tapping a button **edits the message
in place** — so the chat stays one live panel instead of a growing transcript.

With alerts on, the bot messages you when CPU, memory or disk crosses its
threshold — once on the way up and once on recovery, not once per check — and
whenever a tunnel loses or regains its peer.

**The bot only answers the admin IDs you list.** Anyone else is refused and told
nothing but their own Telegram ID. It refuses to start with an empty admin list
rather than answering to the world.

> `/backup` sends live tunnel tokens over Telegram. That is the point of the
> command, but treat the resulting file as a secret.

## Maintenance

### Self-update

backfire can update itself from the latest GitHub release from three places: the
terminal menu (**Update backfire**), the web panel (the **⟳ Update** button) and
the Telegram bot (`/update`). Each checks the running version against the latest
release, and installs it only after you confirm.

The install downloads the prebuilt binary for the host's architecture, verifies
it against the published SHA-256 checksum, and swaps it into place with an
**atomic rename**. Because every already-running process keeps its open copy of
the old binary, **the update never drops a live tunnel** — the panel, the bot and
every tunnel pick up the new version only when you restart them (or on the next
reboot). The terminal flow offers to restart the panel/bot for you and, only if
you agree, the tunnels.

When two servers are linked and one is far behind, the update flow prints a
**mutual-update warning** naming the tunnel and the peer's version, so you know
to update the other end too rather than let the two drift apart. This works
because linked peers exchange their versions during the handshake — an older
peer that predates version reporting still connects and is simply reported as an
older build.

### Optimize server

`sudo backfire` → **Optimize server** applies modern network tuning aimed at
maximum tunnel throughput: BBR congestion control with the `fq` qdisc, large
socket buffers, TCP Fast Open, no slow-start-after-idle, MTU probing, IP
forwarding, a raised connection-tracking table, and file-descriptor limits
lifted to a million. The settings are written as drop-in files
(`/etc/sysctl.d/99-backfire.conf` and `/etc/security/limits.d/99-backfire.conf`),
so the change is easy to review and to revert by deleting them. When a reboot is
needed for everything to take full effect it asks; decline and it reminds you to
reboot later.

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
  server/               transport-mode exposed side: publish ports, hand off connections
  client/               transport-mode origin side: link, reconnect, serve targets
  backhaul/             layer-3 tunnel: TUN device, carriers, frame crypto, spoof
  manage/               systemd units + tunnel lifecycle
  menu/                 interactive CLI
  metrics/              per-tunnel counters, history, cross-process state files
  sysstat/              host CPU / memory / disk / network from /proc
  tlsutil/              shared self-signed / supplied TLS material
  webui/                web panel (embedded, self-contained, HTTP or HTTPS)
  telegram/             bot: commands, keyboards, alerts
  utils/                logger, token, metered pipe helpers
  e2e/                  end-to-end tunnel test across all transports
```

### How the panel sees the tunnels

Each tunnel is its own systemd unit, so the panel and the bot are separate
processes from the engines whose traffic they report and cannot read their
counters directly. Every engine publishes a snapshot to `/run/backfire/<name>.json`
on the sampling interval, and the readers just read that directory. `/run` is a
tmpfs, so a reboot clears it and a tunnel that is no longer running leaves
nothing behind to misreport.

---

## Roadmap

- Forwarding UDP *services* (the udp/kcp transports carry TCP services today)
- A QUIC transport
- Restore-from-backup in the bot
- Signed releases
- Prebuilt release binaries + checksum-verified installs

---

## License

MIT — see [LICENSE](LICENSE).
