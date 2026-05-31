#!/bin/sh
# Weft installer — https://tryweft.app
#
#   curl -fsSL https://tryweft.app/install.sh | sh
#
# Downloads the right prebuilt `weft` binary for your OS/arch, verifies its
# SHA-256, and drops it on your PATH. Pure-Go binary, no dependencies. The
# semantic-embeddings upgrade (`make build-ort`) is optional and built from
# source; everything else — vault, surfacing, BYOC E2EE sync — works from this.
#
# Env overrides:
#   WEFT_DL_BASE  download base URL   (default: https://tryweft.app/dl)
#   WEFT_BIN_DIR  install directory   (default: /usr/local/bin or ~/.local/bin)
#   WEFT_VERSION  version tag to fetch (default: latest)

set -eu

DL_BASE="${WEFT_DL_BASE:-https://tryweft.app/dl}"
VERSION="${WEFT_VERSION:-latest}"

say()  { printf '%s\n' "$*"; }
err()  { printf 'error: %s\n' "$*" >&2; exit 1; }

# --- detect OS + arch ------------------------------------------------------
os=$(uname -s 2>/dev/null || echo unknown)
arch=$(uname -m 2>/dev/null || echo unknown)

case "$os" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  MINGW*|MSYS*|CYGWIN*|Windows_NT)
    err "Windows detected — download weft-windows-amd64.exe from https://tryweft.app/install and add it to your PATH." ;;
  *) err "unsupported OS: $os (mac and linux have a one-line installer; see https://tryweft.app/install)" ;;
esac

case "$arch" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64)  arch=amd64 ;;
  *) err "unsupported architecture: $arch" ;;
esac

bin="weft-${os}-${arch}"
url="${DL_BASE}/${VERSION}/${bin}"

# --- pick a downloader -----------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  dl() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  dl() { wget -qO "$2" "$1"; }
else
  err "need curl or wget to download"
fi

# --- download into a temp dir ---------------------------------------------
tmp=$(mktemp -d 2>/dev/null || mktemp -d -t weft)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $bin ($VERSION)…"
dl "$url" "$tmp/weft" || err "download failed: $url"

# --- verify checksum -------------------------------------------------------
# A mismatch is always fatal. Every path that installs WITHOUT verifying emits a
# visible warning (or hard-fails if WEFT_STRICT_VERIFY=1) — never silent.
strict="${WEFT_STRICT_VERIFY:-0}"
skip_verify() { # $1 = reason
  if [ "$strict" = 1 ]; then err "$1 (WEFT_STRICT_VERIFY=1)"; fi
  say "warning: $1 — installing UNVERIFIED."
}
if dl "${DL_BASE}/${VERSION}/SHA256SUMS" "$tmp/SHA256SUMS" 2>/dev/null; then
  want=$(grep " ${bin}\$" "$tmp/SHA256SUMS" 2>/dev/null | awk '{print $1}')
  if [ -z "${want:-}" ]; then
    skip_verify "SHA256SUMS has no entry for $bin"
  elif command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$tmp/weft" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    got=$(shasum -a 256 "$tmp/weft" | awk '{print $1}')
  else
    skip_verify "no sha256sum/shasum tool found, cannot verify $bin"
  fi
  if [ -n "${want:-}" ] && [ -n "${got:-}" ]; then
    [ "$got" != "$want" ] && err "checksum mismatch for $bin — refusing to install (got $got, want $want)"
    say "Checksum verified."
  fi
else
  skip_verify "no SHA256SUMS published for $VERSION"
fi

chmod +x "$tmp/weft"

# --- choose an install dir on PATH ----------------------------------------
if [ -n "${WEFT_BIN_DIR:-}" ]; then
  dir="$WEFT_BIN_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
  dir=/usr/local/bin
else
  dir="$HOME/.local/bin"
fi
if ! mkdir -p "$dir" 2>/dev/null; then
  if [ "$dir" = /usr/local/bin ] && command -v sudo >/dev/null 2>&1; then
    sudo mkdir -p "$dir"
  else
    err "could not create $dir — set WEFT_BIN_DIR to a writable dir and re-run"
  fi
fi

if mv "$tmp/weft" "$dir/weft" 2>/dev/null; then
  :
elif command -v sudo >/dev/null 2>&1 && [ "$dir" = /usr/local/bin ]; then
  say "Installing to $dir (needs sudo)…"
  sudo mv "$tmp/weft" "$dir/weft"
else
  err "could not write to $dir — set WEFT_BIN_DIR to a writable dir and re-run"
fi

say ""
say "Installed weft to $dir/weft"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "NOTE: $dir is not on your PATH. Add it, e.g.:"
     say "  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.zshrc && source ~/.zshrc" ;;
esac
say ""
say "Get started:"
say "  weft serve ~/notes        # open your vault at http://localhost:7777"
say "  weft sync init            # set up E2EE sync to your own cloud (optional)"
say ""
say "Docs: https://tryweft.app/getting-started"
