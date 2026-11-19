#!/bin/sh
# Install the graft release binary into ~/.local/bin.
# Usage: curl -fsSL https://raw.githubusercontent.com/akuma/graft/main/install.sh | sh
set -eu

repo="akuma/graft"
bindir="${GRAFT_BIN:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$os" in
  darwin|linux) ;;
  *) echo "unsupported OS: $os" >&2; exit 1 ;;
esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

tag="${GRAFT_VERSION:-}"
if [ -z "$tag" ]; then
  tag=$(curl -fsSL "https://api.github.com/repos/${repo}/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
fi
if [ -z "$tag" ]; then
  echo "no release found. Set GRAFT_VERSION=vX.Y.Z" >&2
  exit 1
fi

ver=${tag#v}
name="graft_${ver}_${os}_${arch}"
base="https://github.com/${repo}/releases/download/${tag}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "${base}/${name}.tar.gz" -o "$tmp/graft.tar.gz"
curl -fsSL "${base}/checksums.txt" -o "$tmp/checksums.txt"
expected=$(awk -v f="${name}.tar.gz" '{ n=$2; sub(/^\.\//, "", n); if (n == f) print $1 }' "$tmp/checksums.txt")
if [ -z "$expected" ]; then
  echo "no checksum for ${name}.tar.gz" >&2
  exit 1
fi
if command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/graft.tar.gz" | awk '{ print $1 }')
else
  actual=$(sha256sum "$tmp/graft.tar.gz" | awk '{ print $1 }')
fi
if [ "$actual" != "$expected" ]; then
  echo "checksum mismatch" >&2
  exit 1
fi

tar -xzf "$tmp/graft.tar.gz" -C "$tmp"
mkdir -p "$bindir"
mv "$tmp/graft" "$bindir/graft"
chmod 755 "$bindir/graft"
echo "installed ${bindir}/graft (${tag})"
case ":$PATH:" in
  *":${bindir}:"*) ;;
  *) echo "add ${bindir} to PATH" >&2 ;;
esac
