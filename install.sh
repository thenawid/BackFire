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
set -euo pipefail

RED='\033[0;31m'; GRN='\033[0;32m'; WHT='\033[1;37m'; GRY='\033[0;90m'; NC='\033[0m'
info() { echo -e "${WHT}[*]${NC} $*"; }
ok()   { echo -e "${GRN}[+]${NC} $*"; }
warn() { echo -e "${GRY}[!]${NC} $*"; }
err()  { echo -e "${RED}[x]${NC} $*" >&2; }

REPO="thenawid/BackFire"
BIN_PATH="/usr/local/bin/backfire"
CONFIG_DIR="/etc/backfire"
SRC_DIR="/opt/backfire-src"
GO_VERSION="1.24.5"
GO_MIN_MINOR=24

if [[ $EUID -ne 0 ]]; then err "Please run as root (sudo)."; exit 1; fi

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

# ---- try the prebuilt binary first ------------------------------------------
# Download the binary and its checksum, verify, and install. Any failure here is
# not fatal — we fall through to the source build.
install_prebuilt() {
  local tmp; tmp="$(mktemp -d)"
  info "Downloading prebuilt ${ASSET} from the latest release…"
  if ! curl -fsSL "${BASE}/${ASSET}" -o "${tmp}/${ASSET}"; then
    warn "no prebuilt binary available for ${ARCH}"
    rm -rf "${tmp}"; return 1
  fi
  if curl -fsSL "${BASE}/${ASSET}.sha256" -o "${tmp}/${ASSET}.sha256"; then
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
  curl -fsSL "https://go.dev/dl/${tarball}" -o "/tmp/${tarball}"
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
  if [[ -d "$SRC_DIR/.git" ]]; then
    info "Updating source in ${SRC_DIR}…"
    git -C "$SRC_DIR" pull --ff-only || warn "git pull failed, building existing checkout"
  else
    info "Cloning ${REPO}…"
    rm -rf "$SRC_DIR"
    git clone --depth 1 "https://github.com/${REPO}.git" "$SRC_DIR"
  fi
  info "Building backfire…"
  ( cd "$SRC_DIR" && "$(command -v go)" build -trimpath -o "$BIN_PATH" . )
  chmod +x "$BIN_PATH"
}

if install_prebuilt; then
  ok "Installed prebuilt $("$BIN_PATH" -v)"
else
  build_from_source
  ok "Installed $("$BIN_PATH" -v)"
fi

echo
ok "backfire is ready. Opening the menu — reopen it any time with: sudo backfire"
echo

# Only launch the menu on an interactive terminal.
if [[ -t 0 && -t 1 ]]; then
  exec "$BIN_PATH"
fi
