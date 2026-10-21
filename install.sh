#!/bin/sh
# Install mcli from GitHub Releases (Linux, macOS, and Git Bash/MSYS on Windows).
#
#   curl -fsSL https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.sh | sh
#
# Environment:
#   MCLI_VERSION      release tag to install, e.g. v0.1.0 (default: latest)
#   MCLI_INSTALL_DIR  target directory (default: $HOME/.local/bin)
#   MCLI_REPO         GitHub repository (default: ZeroPointSix/metamcp-cli)
#   MCLI_DOWNLOAD_BASE  override the asset base URL (mirrors, tests)
#
# The archive is verified against the release's checksums.txt (SHA-256)
# before anything is installed. No root privileges are needed.
set -eu

REPO="${MCLI_REPO:-ZeroPointSix/metamcp-cli}"
VERSION="${MCLI_VERSION:-latest}"
INSTALL_DIR="${MCLI_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf 'mcli-install: %s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*) os=windows ;;
  *) die "unsupported OS $(uname -s); download a binary from https://github.com/$REPO/releases" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

if [ "$os" = windows ]; then
  archive="mcli_${os}_${arch}.zip"
  exe=mcli.exe
else
  archive="mcli_${os}_${arch}.tar.gz"
  exe=mcli
fi

if [ -n "${MCLI_DOWNLOAD_BASE:-}" ]; then
  base="$MCLI_DOWNLOAD_BASE"
elif [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
else
  die "curl or wget is required"
fi

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  die "sha256sum or shasum is required to verify the download"
fi

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t mcli)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading $archive ($VERSION)"
fetch "$base/$archive" "$tmp/$archive" || die "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

want="$(awk -v f="$archive" '$2 == f || $2 == "*" f {print $1}' "$tmp/checksums.txt")"
[ -n "$want" ] || die "$archive is not listed in checksums.txt"
got="$(sha256 "$tmp/$archive")"
[ "$want" = "$got" ] || die "checksum mismatch for $archive (expected $want, got $got)"

mkdir -p "$tmp/x"
if [ "$os" = windows ]; then
  command -v unzip >/dev/null 2>&1 || die "unzip is required"
  unzip -q "$tmp/$archive" -d "$tmp/x"
else
  tar -xzf "$tmp/$archive" -C "$tmp/x"
fi
[ -f "$tmp/x/$exe" ] || die "$exe not found in $archive"

mkdir -p "$INSTALL_DIR"
# Install via a temporary name and rename, so a running daemon keeps its binary.
cp "$tmp/x/$exe" "$INSTALL_DIR/.$exe.new"
chmod 755 "$INSTALL_DIR/.$exe.new"
mv -f "$INSTALL_DIR/.$exe.new" "$INSTALL_DIR/$exe"

installed="$("$INSTALL_DIR/$exe" --version 2>/dev/null || true)"
say "installed ${installed:-mcli} to $INSTALL_DIR/$exe"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say "note: $INSTALL_DIR is not on PATH; add it, e.g. export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
