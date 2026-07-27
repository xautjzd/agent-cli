#!/usr/bin/env bash
# agent-cli installer — downloads the latest release binary from GitHub.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/xautjzd/agent-cli/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/xautjzd/agent-cli/main/install.sh | bash -s -- --version 0.2.0
#
# Options:
#   -h, --help              Print this help
#   -v, --version <ver>     Install a specific version (e.g. 0.2.0)
#   -d, --install-dir <dir> Install to <dir> (default: ~/.local/bin, or /usr/local/bin if sudo)
set -euo pipefail

REPO="xautjzd/agent-cli"
BINARY_NAME="agent"

# --- ANSI colors (disabled when not a TTY) ---
if [ -t 1 ]; then
    BOLD='\033[1m'
    GREEN='\033[0;32m'
    RED='\033[0;31m'
    YELLOW='\033[0;33m'
    MUTED='\033[0;2m'
    NC='\033[0m'
else
    BOLD='' GREEN='' RED='' YELLOW='' MUTED='' NC=''
fi

info()  { printf "%b%s%b\n"   "$GREEN" "$1" "$NC"; }
warn()  { printf "%b%s%b\n"   "$YELLOW" "$1" "$NC"; }
err()   { printf "%b%s%b\n"   "$RED" "$1" "$NC" >&2; }
step()  { printf "%b==>%b %s\n" "$BOLD" "$NC" "$1"; }

usage() {
    cat <<EOF
agent-cli installer

Usage: install.sh [options]

Options:
  -h, --help              Print this help
  -v, --version <ver>     Install a specific version (e.g. 0.2.0)
  -d, --install-dir <dir> Install to <dir> (default: ~/.local/bin, or /usr/local/bin if sudo)
EOF
}

# --- Parse arguments ---
VERSION=""
INSTALL_DIR=""
while [ $# -gt 0 ]; do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        -v|--version) shift; VERSION="$1"; shift ;;
        -d|--install-dir) shift; INSTALL_DIR="$1"; shift ;;
        *) err "Unknown option: $1"; usage; exit 1 ;;
    esac
done

# --- Detect OS and architecture ---
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
    darwin) OS="darwin" ;;
    linux)  OS="linux" ;;
    *) err "Unsupported OS: $OS (this installer supports darwin and linux)"; exit 1 ;;
esac
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *) err "Unsupported architecture: $ARCH (this installer supports amd64 and arm64)"; exit 1 ;;
esac

# --- Resolve install directory ---
if [ -z "$INSTALL_DIR" ]; then
    if [ "$(id -u)" -eq 0 ]; then
        INSTALL_DIR="/usr/local/bin"
    else
        INSTALL_DIR="${HOME}/.local/bin"
    fi
fi

# --- Resolve version ---
if [ -z "$VERSION" ]; then
    step "Fetching latest release from GitHub…"
    # GitHub API returns tag_name like "v0.1.0"; strip the leading "v".
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep -o '"tag_name":[[:space:]]*"v[^"]*"' \
        | head -1 \
        | sed 's/.*"tag_name":[[:space:]]*"v\([^"]*\)".*/\1/')
    if [ -z "$VERSION" ]; then
        err "Could not determine the latest release. Check your internet connection or specify --version."
        exit 1
    fi
fi

# GoReleaser strips the leading "v" from tag names for archive file names
# (version is "0.1.0", not "v0.1.0").
ARCHIVE="agent-cli_${VERSION}_${OS}_${ARCH}.tar.gz"
ARCHIVE_URL="https://github.com/${REPO}/releases/download/v${VERSION}/${ARCHIVE}"

# --- Download ---
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

step "Downloading ${ARCHIVE}…"
if ! curl -fSL -o "${TMP_DIR}/${ARCHIVE}" "$ARCHIVE_URL"; then
    # Show available assets to help the user pick a valid version/platform.
    err "Download failed for: ${ARCHIVE_URL}"
    warn "This can happen if:"
    warn "  - The version ${VERSION} doesn't exist"
    warn "  - No binary was published for ${OS}/${ARCH}"
    err "Check available versions at: https://github.com/${REPO}/releases"
    exit 1
fi

# --- Extract ---
step "Extracting…"
tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "$TMP_DIR"
BINARY_PATH="${TMP_DIR}/${BINARY_NAME}"
if [ ! -f "$BINARY_PATH" ]; then
    err "Binary '${BINARY_NAME}' not found in the archive."
    exit 1
fi

# --- Install ---
step "Installing to ${INSTALL_DIR}/…"
mkdir -p "$INSTALL_DIR"
mv "$BINARY_PATH" "${INSTALL_DIR}/${BINARY_NAME}"
chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

# --- PATH management ---
add_to_path() {
    local shell_rc=""
    case "$(basename "$SHELL")" in
        zsh)  shell_rc="${HOME}/.zshrc" ;;
        bash) shell_rc="${HOME}/.bashrc" ;;
        *) return ;;
    esac
    if [ -f "$shell_rc" ] && ! grep -q "$INSTALL_DIR" "$shell_rc"; then
        printf '\n# Added by agent-cli installer\nexport PATH="%s:$PATH"\n' "$INSTALL_DIR" >> "$shell_rc"
        warn "Added $INSTALL_DIR to PATH in $shell_rc"
        warn "Run: source $shell_rc  (or open a new terminal)"
    fi
}

if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
    add_to_path
fi

# --- Done ---
printf "\n"
info "agent-cli v${VERSION} installed!"
printf "%b  →%b %s/%s\n" "$BOLD" "$NC" "$INSTALL_DIR" "$BINARY_NAME"
printf "%b  →%b Run 'agent version' to verify.\n" "$BOLD" "$NC"
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
    printf "\n"
    warn "Note: $INSTALL_DIR is not in your current PATH."
    warn "Start a new terminal or run: export PATH=\"$INSTALL_DIR:\$PATH\""
fi
