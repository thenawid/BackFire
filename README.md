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

- **Multi-transport.** `tcp` for raw speed, `ws` to ride through CDNs and layer-7
  proxies, `wss` for the same plus TLS. Same tunnel, one keyword to switch.
- **Stream multiplexing.** All forwarded connections share one link via
  [smux](https://github.com/xtaci/smux), with per-stream flow control — no
  connection storms, no per-connection handshake tax.
- **Authenticated, replay-resistant handshake.** Peers prove a shared token with
  an HMAC-SHA256 challenge/response; the token never crosses the wire, even on a
  plain `tcp` link.
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

| Transport | Encrypted | Blends in with | Use when |
|---|---|---|---|
| `tcp` | no | nothing | the path is clean and you want lowest overhead |
| `ws`  | no | HTTP/WebSocket | a CDN or L7 proxy sits in front |
| `wss` | yes (TLS) | HTTPS/WebSocket | you want encryption and CDN compatibility |

For `wss`, leaving `tls_cert`/`tls_key` empty generates an in-memory self-signed
certificate — that's fine, because peers authenticate by the **token**, not the
certificate chain. Point them at a real certificate if you terminate a named
domain.

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
config/                 TOML schema, parsing, validation
cmd/                    default configs for the menu
internal/
  app/                  version, paths, engine dispatch
  protocol/             token handshake + stream target framing
  transport/            tcp, ws, wss (net.Conn / net.Listener providers)
  mux/                  smux wrapper
  server/               exposed side: publish ports, open streams
  client/               origin side: dial, reconnect, serve streams
  manage/               systemd units + tunnel lifecycle
  menu/                 interactive CLI
  utils/                logger, token, pipe helpers
  e2e/                  end-to-end tunnel test
```

---

## Roadmap

- More transports (KCP/QUIC for lossy paths), UDP forwarding
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
