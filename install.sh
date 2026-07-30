#!/usr/bin/env bash
#
# backfire installer — run once on the VPS as root:
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/thenawid/backfire/main/install.sh)
#
# It builds the single backfire binary from source (installing the Go toolchain
# if the machine has none new enough), installs it to /usr/local/bin/backfire,
# creates /etc/backfire for tunnel configs, and then opens the interactive menu.
#
# Reopen the menu any time with:   sudo backfire
#
set -euo pipefail

RED='\033[0;31m'; GRN='\033[0;32m'; WHT='\033[1;37m'; GRY='\033[0;90m'; NC='\033[0m'
info() { echo -e "${WHT}[*]${NC} $*"; }
ok()   { echo -e "${GRN}[+]${NC} $*"; }
warn() { echo -e "${GRY}[!]${NC} $*"; }
err()  { echo -e "${RED}[x]${NC} $*" >&2; }

REPO="thenawid/backfire"
BIN_PATH="/usr/local/bin/backfire"
CONFIG_DIR="/etc/backfire"
SRC_DIR="/opt/backfire-src"
GO_VERSION="1.24.5"
GO_MIN_MINOR=24

if [[ $EUID -ne 0 ]]; then err "Please run as root (sudo)."; exit 1; fi

case "$(uname -m)" in
  x86_64|amd64)  GOARCH="amd64" ;;
  aarch64|arm64) GOARCH="arm64" ;;
  *) err "Unsupported architecture: $(uname -m)"; exit 1 ;;
esac

mkdir -p "$CONFIG_DIR"

# ---- ensure a usable Go toolchain -------------------------------------------
have_go() {
  command -v go >/dev/null 2>&1 || return 1
  local minor
  minor="$(go version | sed -n 's/.*go1\.\([0-9]*\).*/\1/p')"
  [[ -n "$minor" && "$minor" -ge "$GO_MIN_MINOR" ]]
}

install_go() {
  info "Installing Go ${GO_VERSION} for ${GOARCH}…"
  local tarball="go${GO_VERSION}.linux-${GOARCH}.tar.gz"
  curl -fsSL "https://go.dev/dl/${tarball}" -o "/tmp/${tarball}"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "/tmp/${tarball}"
  rm -f "/tmp/${tarball}"
  export PATH="/usr/local/go/bin:$PATH"
}

if have_go; then
  ok "Found $(go version)"
else
  install_go
  export PATH="/usr/local/go/bin:$PATH"
fi

# ---- fetch source and build -------------------------------------------------
if command -v git >/dev/null 2>&1; then
  if [[ -d "$SRC_DIR/.git" ]]; then
    info "Updating source in ${SRC_DIR}…"
    git -C "$SRC_DIR" pull --ff-only || warn "git pull failed, building existing checkout"
  else
    info "Cloning ${REPO}…"
    rm -rf "$SRC_DIR"
    git clone --depth 1 "https://github.com/${REPO}.git" "$SRC_DIR"
  fi
else
  err "git is required to fetch the source. Install git and re-run."
  exit 1
fi

info "Building backfire…"
( cd "$SRC_DIR" && /usr/local/go/bin/go build -trimpath -o "$BIN_PATH" . 2>/dev/null \
    || go build -trimpath -o "$BIN_PATH" . )
chmod +x "$BIN_PATH"
ok "Installed $("$BIN_PATH" -v)"

echo
ok "backfire is ready. Opening the menu — reopen it any time with: sudo backfire"
echo

# Only launch the menu on an interactive terminal.
if [[ -t 0 && -t 1 ]]; then
  exec "$BIN_PATH"
fi
