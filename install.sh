#!/usr/bin/env bash
# Installs the latest hew release binary to $HOME/.local/bin (override
# with INSTALL_DIR). Never uses sudo. Set HEW_VERSION (1.2.3 or v1.2.3)
# to install that release instead of the latest.
#
#   curl -fsSL https://lumberbarons.github.io/hew/install.sh | bash
#   curl -fsSL https://lumberbarons.github.io/hew/install.sh | HEW_VERSION=1.2.3 bash
set -euo pipefail

REPO="lumberbarons/hew"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux | darwin) ;;
  *)
    echo "unsupported OS: $os (use: go install github.com/$REPO/cmd/hew@latest)" >&2
    exit 1
    ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *)
    echo "unsupported architecture: $arch (use: go install github.com/$REPO/cmd/hew@latest)" >&2
    exit 1
    ;;
esac

if [ -n "${HEW_VERSION:-}" ]; then
  # A pinned version skips the releases API entirely (and its unauthenticated
  # rate limit). The value lands in a URL, so reject anything but a semver.
  version="${HEW_VERSION#v}"
  if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$'; then
    echo "invalid HEW_VERSION: $HEW_VERSION (expected e.g. 1.2.3 or v1.2.3)" >&2
    exit 1
  fi
  tag="v$version"
else
  release_json=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest")
  tag=$(printf '%s' "$release_json" | grep -m1 '"tag_name"' | cut -d'"' -f4)
  if [ -z "$tag" ]; then
    echo "cannot resolve the latest release of $REPO" >&2
    exit 1
  fi
  version="${tag#v}"
fi

archive="hew_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading hew $tag ($os/$arch)..."
if ! curl -fsSL -o "$tmp/$archive" "$base/$archive"; then
  echo "cannot download hew $tag for $os/$arch from $base (is $version a published release?)" >&2
  exit 1
fi
if ! curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"; then
  echo "cannot download checksums.txt for hew $tag from $base" >&2
  exit 1
fi

(
  cd "$tmp"
  expected=$(grep " $archive\$" checksums.txt | cut -d' ' -f1)
  if [ -z "$expected" ]; then
    echo "no checksum for $archive in checksums.txt" >&2
    exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$archive" | cut -d' ' -f1)
  else
    actual=$(shasum -a 256 "$archive" | cut -d' ' -f1)
  fi
  if [ "$expected" != "$actual" ]; then
    echo "checksum mismatch for $archive" >&2
    exit 1
  fi
)

tar -xzf "$tmp/$archive" -C "$tmp" hew
mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/hew" "$INSTALL_DIR/hew"
echo "installed $("$INSTALL_DIR/hew" --version) to $INSTALL_DIR/hew"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "note: $INSTALL_DIR is not on your PATH" ;;
esac
