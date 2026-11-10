#!/bin/sh
# Install the talksasa CLI on Linux or macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/samuelmacharia99/talksasa-cli/main/install.sh | sh
#
# Downloads the latest release (or TALKSASA_VERSION, such as v0.1.0) from
# GitHub, checks it against the release's checksums.txt, and installs it to
# TALKSASA_INSTALL_DIR (default: /usr/local/bin when writable, else
# ~/.local/bin). Nothing is installed if the checksum does not match.
set -eu

repo="samuelmacharia99/talksasa-cli"
version="${TALKSASA_VERSION:-}"

fail() { echo "talksasa install: $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || fail "needs $1"; }
need uname
need tar

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"; }
  fetch_stdout() { curl -fsSL --proto '=https' --tlsv1.2 "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
  fetch_stdout() { wget -q -O - "$1"; }
else
  fail "needs curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  fail "needs sha256sum or shasum to check the download"
fi

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "this script is for Linux and macOS; on Windows, download the zip from https://github.com/$repo/releases" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no build for $(uname -m)" ;;
esac

if [ -z "$version" ]; then
  version=$(fetch_stdout "https://api.github.com/repos/$repo/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$version" ] || fail "could not find the latest release"
fi
case "$version" in
  v*) ;;
  *) version="v$version" ;;
esac
number="${version#v}"

archive="talksasa_${number}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading talksasa $version ($os/$arch)..."
fetch "$base/$archive" "$tmp/$archive" || fail "could not download $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "could not download checksums.txt"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || fail "checksums.txt has no entry for $archive"
actual=$(sha256 "$tmp/$archive")
[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive: expected $expected, got $actual. Nothing was installed."

tar -xzf "$tmp/$archive" -C "$tmp" talksasa

dir="${TALKSASA_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  else
    dir="$HOME/.local/bin"
  fi
fi
mkdir -p "$dir"
install -m 0755 "$tmp/talksasa" "$dir/talksasa" 2>/dev/null || {
  cp "$tmp/talksasa" "$dir/talksasa" && chmod 0755 "$dir/talksasa"
}

echo "Installed $("$dir/talksasa" version) to $dir/talksasa"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Add $dir to your PATH to run it as: talksasa" ;;
esac
echo "Next: talksasa login"
