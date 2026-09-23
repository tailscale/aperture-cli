#!/bin/sh
# Install aperture from the latest GitHub release: detect the platform,
# download the matching asset, verify it against the release checksums and
# install it as `aperture`. curl installs never carry the quarantine
# attribute, so Gatekeeper never assesses the result.
#
#   curl -fsSL https://raw.githubusercontent.com/tailscale/aperture-cli/main/install.sh | sh
#
# APERTURE_VERSION pins a release tag (default: latest), APERTURE_INSTALL_DIR
# overrides /usr/local/bin.
set -eu

REPO=tailscale/aperture-cli
INSTALL_DIR="${APERTURE_INSTALL_DIR:-/usr/local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
	x86_64) arch=amd64 ;;
	arm64|aarch64) arch=arm64 ;;
	*) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

if [ -z "${APERTURE_VERSION:-}" ]; then
	redirect=$(curl -fsSI -o /dev/null -w '%{redirect_url}' "https://github.com/$REPO/releases/latest")
	[ -n "$redirect" ] || { echo "could not resolve the latest release" >&2; exit 1; }
	APERTURE_VERSION=${redirect##*/}
fi

case "$os" in
	linux) asset="aperture-cli_linux_$arch.tar.gz" ;;
	darwin) asset="aperture_${APERTURE_VERSION}_darwin_$arch.zip" ;;
	*) echo "unsupported OS: $os" >&2; exit 1 ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

base="https://github.com/$REPO/releases/download/$APERTURE_VERSION"
echo "==> Downloading $asset ($APERTURE_VERSION)"
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

echo "==> Verifying checksum"
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$tmp" && grep " ${asset}$" checksums.txt | sha256sum -c -)
else
	(cd "$tmp" && grep " ${asset}$" checksums.txt | shasum -a 256 -c -)
fi

echo "==> Extracting"
mkdir "$tmp/x"
case "$asset" in
	*.tar.gz) tar -xzf "$tmp/$asset" -C "$tmp/x" ;;
	*.zip) unzip -q "$tmp/$asset" -d "$tmp/x" ;;
esac
bin=$(find "$tmp/x" -type f -name 'aperture*' ! -name '._*' | head -1)
[ -n "$bin" ] || { echo "no aperture binary found in $asset" >&2; exit 1; }

if [ ! -d "$INSTALL_DIR" ]; then
	mkdir -p "$INSTALL_DIR" 2>/dev/null || true
fi
sudo=""
if [ ! -w "$INSTALL_DIR" ]; then
	command -v sudo >/dev/null 2>&1 || {
		echo "cannot write $INSTALL_DIR; set APERTURE_INSTALL_DIR (e.g. \$HOME/.local/bin)" >&2
		exit 1
	}
	sudo="sudo"
fi
$sudo mkdir -p "$INSTALL_DIR"
$sudo install -m 0755 "$bin" "$INSTALL_DIR/aperture"

echo "==> Installed $("$INSTALL_DIR/aperture" -version | head -1) to $INSTALL_DIR/aperture"
