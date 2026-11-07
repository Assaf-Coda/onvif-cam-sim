#!/bin/sh
# Installs the latest onvif-cam-sim release binary on macOS or Linux.
#   curl -fsSL https://raw.githubusercontent.com/Assaf-Coda/onvif-cam-sim/main/install.sh | sh
# Environment: VERSION (tag, default latest), BIN_DIR (default /usr/local/bin, falls back to ~/.local/bin).
set -eu

REPO=Assaf-Coda/onvif-cam-sim
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
case "$OS" in
  darwin|linux) ;;
  *) echo "unsupported OS: $OS (on Windows use install.ps1)" >&2; exit 1 ;;
esac

VERSION=${VERSION:-$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')}
[ -n "$VERSION" ] || { echo "could not determine latest release" >&2; exit 1; }
NAME="onvif-cam-sim_${VERSION}_${OS}_${ARCH}"
BASE="https://github.com/$REPO/releases/download/$VERSION"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
echo "downloading $NAME.tar.gz"
curl -fsSL "$BASE/$NAME.tar.gz" -o "$TMP/$NAME.tar.gz"
curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt"
cd "$TMP"
if command -v sha256sum >/dev/null 2>&1; then
  grep " $NAME.tar.gz\$" checksums.txt | sha256sum -c - >/dev/null
else
  grep " $NAME.tar.gz\$" checksums.txt | shasum -a 256 -c - >/dev/null
fi
tar -xzf "$NAME.tar.gz"

BIN_DIR=${BIN_DIR:-/usr/local/bin}
if ! mkdir -p "$BIN_DIR" 2>/dev/null || [ ! -w "$BIN_DIR" ]; then
  BIN_DIR="$HOME/.local/bin"
  mkdir -p "$BIN_DIR"
fi
install -m 0755 "$NAME/onvif-cam-sim" "$BIN_DIR/onvif-cam-sim"
echo "installed $VERSION to $BIN_DIR/onvif-cam-sim"
case ":$PATH:" in *":$BIN_DIR:"*) ;; *) echo "note: $BIN_DIR is not on your PATH" ;; esac
command -v ffmpeg >/dev/null 2>&1 || echo "note: ffmpeg not found; install it (brew install ffmpeg / apt install ffmpeg) before running"
echo "next: curl -fsSL https://raw.githubusercontent.com/$REPO/main/config.example.yaml -o config.yaml && onvif-cam-sim -check"
