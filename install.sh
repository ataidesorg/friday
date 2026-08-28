#!/usr/bin/env bash
# Install the latest Friday release for macOS, Linux, WSL, or Git Bash.
set -euo pipefail

repo="${FRIDAY_REPO:-ataidesorg/friday}"
version="${FRIDAY_VERSION:-latest}"

fail() {
  echo "friday install: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

download() {
  url="$1"
  out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$out"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -q "$url" -O "$out"
    return
  fi
  fail "curl or wget is required"
}

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) fail "unsupported OS: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

install_dir="${FRIDAY_INSTALL_DIR:-}"
if [ -z "$install_dir" ] && [ -n "${XDG_BIN_DIR:-}" ]; then
  install_dir="$XDG_BIN_DIR"
fi
if [ -z "$install_dir" ]; then
  install_dir="$HOME/bin"
fi
if ! mkdir -p "$install_dir" >/dev/null 2>&1; then
  install_dir="$HOME/.friday/bin"
  mkdir -p "$install_dir"
fi

asset="friday_${version}_${os}_${arch}.tar.gz"
base="https://github.com/${repo}/releases"
if [ "$version" = "latest" ]; then
  asset="friday_${os}_${arch}.tar.gz"
  url="${base}/latest/download/${asset}"
  sums_url="${base}/latest/download/checksums.txt"
else
  url="${base}/download/${version}/${asset}"
  sums_url="${base}/download/${version}/checksums.txt"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

archive="$tmp/$asset"
checksums="$tmp/checksums.txt"

echo "Installing Friday ${version} for ${os}/${arch}"
download "$url" "$archive"
download "$sums_url" "$checksums"

sum_line="$(grep " ${asset}$" "$checksums" || true)"
[ -n "$sum_line" ] || fail "checksums.txt has no entry for ${asset}"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp" && printf '%s\n' "$sum_line" | sha256sum -c -) || fail "checksum verification failed"
elif command -v shasum >/dev/null 2>&1; then
  (cd "$tmp" && printf '%s\n' "$sum_line" | shasum -a 256 -c -) || fail "checksum verification failed"
else
  fail "sha256sum or shasum is required to verify the download"
fi

need tar
mkdir -p "$tmp/unpacked"
tar -xzf "$archive" -C "$tmp/unpacked"

bin="friday"

# Archives contain friday_<version>_<os>_<arch>/$bin. The latest alias
# tarball keeps that inner directory name, so accept exactly one match.
matches=("$tmp/unpacked"/*/"$bin")
if [ "${#matches[@]}" -ne 1 ] || [ ! -f "${matches[0]}" ]; then
  fail "archive did not contain exactly one $bin"
fi
src="${matches[0]}"

cp "$src" "$install_dir/$bin"
chmod 0755 "$install_dir/$bin"

echo "Installed $install_dir/$bin"
if [ ! -x "$install_dir/$bin" ]; then
  fail "installed binary is not executable"
fi
"$install_dir/$bin" version 2>/dev/null || true
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to your PATH, then run: friday version" ;;
esac
