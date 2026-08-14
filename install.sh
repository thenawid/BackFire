#!/usr/bin/env bash
#
# backfire installer — run once on the VPS as root:
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/thenawid/BackFire/main/install.sh)
#
# It downloads the prebuilt binary for this architecture from the latest GitHub
# release, verifies it against the published SHA-256 checksum, installs it to
# /usr/local/bin/backfire, and opens the interactive menu. No Go toolchain and no
# compiling — a normal install is a few-second download.
#
# If no prebuilt binary is available (no release yet, or an unusual arch) it
# falls back to building from source, installing Go if needed.
#
# Reopen the menu any time with:   sudo backfire
#
# Uninstall everything backfire installed:
#   sudo bash <(curl -fsSL https://raw.githubusercontent.com/thenawid/BackFire/main/install.sh) uninstall
#
set -euo pipefail

RED='\033[0;31m'; GRN='\033[0;32m'; WHT='\033[1;37m'; GRY='\033[0;90m'; NC='\033[0m'
info() { echo -e "${WHT}[*]${NC} $*"; }
ok()   { echo -e "${GRN}[+]${NC} $*"; }
warn() { echo -e "${GRY}[!]${NC} $*"; }
err()  { echo -e "${RED}[x]${NC} $*" >&2; }

REPO="thenawid/BackFire"
BIN_PATH="/usr/local/bin/backfire"
CONFIG_DIR="/etc/backfire"
STATE_DIR="/run/backfire"
SRC_DIR="/opt/backfire-src"
SYSCTL_DROPIN="/etc/sysctl.d/99-backfire.conf"
LIMITS_DROPIN="/etc/security/limits.d/99-backfire.conf"
GO_VERSION="1.24.5"
GO_MIN_MINOR=24

if [[ $EUID -ne 0 ]]; then err "Please run as root (sudo)."; exit 1; fi

# ---- uninstall --------------------------------------------------------------
# Remove backfire and everything it installed. Runs entirely from the shell, so
# it works even if the binary is broken. Invoke with:
#   sudo bash install.sh uninstall        (or add -y to skip the prompt)
uninstall_all() {
  local assume_yes="${1:-}"
  info "This will remove backfire and EVERYTHING it installed:"
  echo "  • every backfire-*.service tunnel/panel/bot unit"
  echo "  • the optimization drop-ins ($SYSCTL_DROPIN, $LIMITS_DROPIN)"
  echo "  • all configs and tokens in $CONFIG_DIR"
  echo "  • the state in $STATE_DIR and any source tree in $SRC_DIR"
  echo "  • the binary at $BIN_PATH"
  echo

  if [[ "$assume_yes" != "-y" && "$assume_yes" != "--yes" ]]; then
    if [[ -t 0 ]]; then
      read -r -p "Type 'yes' to remove all of it: " reply
      [[ "$reply" == "yes" ]] || { warn "Cancelled — nothing was removed."; exit 0; }
    else
      err "Refusing to uninstall non-interactively without -y. Re-run: install.sh uninstall -y"
      exit 1
    fi
  fi

  # Stop, disable and delete every backfire unit.
  shopt -s nullglob
  for unit in /etc/systemd/system/backfire-*.service; do
    local name; name="$(basename "$unit")"
    systemctl stop "$name" 2>/dev/null || true
    systemctl disable "$name" 2>/dev/null || true
    rm -f "$unit"
    ok "removed unit $name"
  done
  shopt -u nullglob
  systemctl daemon-reload 2>/dev/null || true

  # Optimization drop-ins, then reload so they stop being applied on boot.
  rm -f "$SYSCTL_DROPIN" "$LIMITS_DROPIN"
  sysctl --system >/dev/null 2>&1 || true
  ok "removed optimization drop-ins"

  # Configs, state, source tree, and finally the binary.
  rm -rf "$CONFIG_DIR" "$STATE_DIR" "$SRC_DIR"
  ok "removed $CONFIG_DIR, $STATE_DIR and $SRC_DIR"
  rm -f "$BIN_PATH"
  ok "removed $BIN_PATH"

  echo
  ok "backfire has been completely uninstalled."
  exit 0
}

case "${1:-}" in
  uninstall|--uninstall|remove) uninstall_all "${2:-}" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) err "Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

mkdir -p "$CONFIG_DIR"
# Engines publish their live state here for the panel and the bot to read.
# /run is a tmpfs, so this is recreated on boot by the engines themselves.
mkdir -p /run/backfire

ASSET="backfire_linux_${ARCH}"
BASE="https://github.com/${REPO}/releases/latest/download"

# ---- downloads (Iran-friendly) ----------------------------------------------
# From some networks — notably Iran — github.com and its release CDN are
# filtered, so a plain download connects and then hangs forever. Two defences:
#   1. every download has a stall detector, so it fails fast instead of hanging;
#   2. GitHub downloads can be routed through a proxy. Point BACKFIRE_MIRROR at a
#      GitHub proxy prefix and the github URL is appended to it, e.g.
#         BACKFIRE_MIRROR="https://ghproxy.net/" sudo -E bash install.sh
#      When no mirror is set and a direct download stalls, a few public proxies
#      are tried automatically.
MIRROR="${BACKFIRE_MIRROR:-}"
DEFAULT_MIRRORS=( "https://ghfast.top/" "https://ghproxy.net/" "https://mirror.ghproxy.com/" )

# _curl adds a connect timeout and a stall detector: abort if the transfer stays
# under 2 KB/s for 20s, which is exactly how a filtered link fails (it connects,
# then goes silent) rather than erroring outright.
_curl() { curl -fSL --connect-timeout 20 --speed-limit 2048 --speed-time 20 --retry 2 --retry-delay 3 "$@"; }

# fetch <github-url> <out>: try the configured mirror (or a direct download),
# then fall back to the default proxies. Non-zero only when every route fails.
fetch() {
  local url="$1" out="$2" p; local -a routes
  if [[ -n "$MIRROR" ]]; then routes=( "$MIRROR" "" ); else routes=( "" "${DEFAULT_MIRRORS[@]}" ); fi
  for p in "${routes[@]}"; do
    [[ -n "$p" ]] && info "trying GitHub proxy ${p}…"
    if _curl "${p}${url}" -o "$out"; then return 0; fi
  done
  return 1
}

# ---- try the prebuilt binary first ------------------------------------------
# Download the binary and its checksum, verify, and install. Any failure here is
# not fatal — we fall through to the source build.
install_prebuilt() {
  local tmp; tmp="$(mktemp -d)"
  info "Downloading prebuilt ${ASSET} from the latest release…"
  if ! fetch "${BASE}/${ASSET}" "${tmp}/${ASSET}"; then
    warn "could not download the prebuilt binary (blocked, offline, or none for ${ARCH})"
    rm -rf "${tmp}"; return 1
  fi
  if fetch "${BASE}/${ASSET}.sha256" "${tmp}/${ASSET}.sha256"; then
    info "Verifying checksum…"
    # The checksum file records the bare asset name, so verify from its dir.
    if ! ( cd "${tmp}" && sha256sum -c "${ASSET}.sha256" >/dev/null 2>&1 ); then
      err "checksum verification failed — refusing to install this download"
      rm -rf "${tmp}"; return 1
    fi
    ok "Checksum verified."
  else
    warn "no checksum published; skipping verification"
  fi
  install -m 0755 "${tmp}/${ASSET}" "$BIN_PATH"
  rm -rf "${tmp}"
  return 0
}

# ---- source build (fallback) ------------------------------------------------
have_go() {
  command -v go >/dev/null 2>&1 || return 1
  local minor
  minor="$(go version | sed -n 's/.*go1\.\([0-9]*\).*/\1/p')"
  [[ -n "$minor" && "$minor" -ge "$GO_MIN_MINOR" ]]
}

install_go() {
  info "Installing Go ${GO_VERSION} for ${ARCH}…"
  local tarball="go${GO_VERSION}.linux-${ARCH}.tar.gz"
  _curl "https://go.dev/dl/${tarball}" -o "/tmp/${tarball}"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "/tmp/${tarball}"
  rm -f "/tmp/${tarball}"
  export PATH="/usr/local/go/bin:$PATH"
}

build_from_source() {
  warn "Falling back to building from source."
  if have_go; then
    ok "Found $(go version)"
  else
    install_go
    export PATH="/usr/local/go/bin:$PATH"
  fi
  if ! command -v git >/dev/null 2>&1; then
    err "git is required to build from source. Install git and re-run."
    exit 1
  fi
  # Route the clone through the mirror when one is set, and give git the same
  # stall detector so a filtered link fails fast instead of hanging.
  export GIT_HTTP_LOW_SPEED_LIMIT=2048 GIT_HTTP_LOW_SPEED_TIME=20
  local clone_url="${MIRROR}https://github.com/${REPO}.git"
  if [[ -d "$SRC_DIR/.git" ]]; then
    info "Updating source in ${SRC_DIR}…"
    git -C "$SRC_DIR" pull --ff-only || warn "git pull failed, building existing checkout"
  else
    info "Cloning ${REPO}…"
    rm -rf "$SRC_DIR"
    git clone --depth 1 "$clone_url" "$SRC_DIR"
  fi
  info "Building backfire…"
  ( cd "$SRC_DIR" && "$(command -v go)" build -trimpath -o "$BIN_PATH" . )
  chmod +x "$BIN_PATH"
}

if install_prebuilt; then
  ok "Installed prebuilt $("$BIN_PATH" -v)"
elif build_from_source && [[ -x "$BIN_PATH" ]]; then
  ok "Installed $("$BIN_PATH" -v)"
else
  err "Could not download or build backfire — GitHub is likely filtered on this server."
  echo
  warn "From Iran, try one of these:"
  echo "  • Route through a GitHub proxy, then re-run:"
  echo "      BACKFIRE_MIRROR=\"https://ghproxy.net/\" sudo -E bash install.sh"
  echo "    (try another proxy if that one is down, e.g. https://ghfast.top/)"
  echo "  • Or install on the ABROAD server first (no filter there), then copy"
  echo "    /usr/local/bin/backfire to this server with scp."
  exit 1
fi

echo
ok "backfire is ready. Opening the menu — reopen it any time with: sudo backfire"
echo

# Only launch the menu on an interactive terminal.
if [[ -t 0 && -t 1 ]]; then
  exec "$BIN_PATH"
fi
