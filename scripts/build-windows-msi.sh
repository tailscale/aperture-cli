#!/usr/bin/env bash
# build-windows-msi.sh <exe> <tag> <arch> <out> packages one signed
# aperture.exe into installer/aperture.wxs's MSI with WiX. <arch> is the Go
# architecture, amd64 or arm64. The exe must already be signed: signing the
# MSI does not sign what is inside it, and once it is built the payload cannot
# change.
#
# MSI versions are x.y.z with fields capped at 255.255.65535 and no suffix, so
# only a stable vX.Y.Z tag packages. Anything else fails rather than ship an
# MSI whose version Windows Installer would misorder.
#
# Requires wix on PATH; scripts/setup-wix.sh installs the pinned one.
set -euo pipefail

exe=${1:?usage: build-windows-msi.sh <exe> <tag> <arch> <out>}
tag=${2:?missing tag}
arch=${3:?missing arch}
out=${4:?missing out}

if [[ ! "$tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
  echo "tag $tag is not a stable vX.Y.Z release; no MSI" >&2
  exit 1
fi
# 10# keeps a leading zero from reading as octal.
if (( 10#${BASH_REMATCH[1]} > 255 || 10#${BASH_REMATCH[2]} > 255 || 10#${BASH_REMATCH[3]} > 65535 )); then
  echo "tag $tag is beyond the MSI version range 255.255.65535" >&2
  exit 1
fi
version=$((10#${BASH_REMATCH[1]})).$((10#${BASH_REMATCH[2]})).$((10#${BASH_REMATCH[3]}))

case "$arch" in
  amd64) platform=x64 ;;
  arm64) platform=arm64 ;;
  *) echo "unsupported architecture $arch: want amd64 or arm64" >&2; exit 1 ;;
esac

[ -f "$exe" ] || { echo "$exe not found" >&2; exit 1; }

wxs="$(dirname "$0")/../installer/aperture.wxs"
# wix.exe is a Windows program; Git Bash's /d/a/... paths mean nothing to it.
if command -v cygpath >/dev/null; then
  exe=$(cygpath -w "$exe") out=$(cygpath -w "$out") wxs=$(cygpath -w "$wxs")
fi

# Tailscale sponsors wixtoolset, which is what the OSMF EULA asks of it.
wix build -acceptEula wix7 -arch "$platform" \
  -d Version="$version" -d SourceExe="$exe" \
  -o "$out" "$wxs"
