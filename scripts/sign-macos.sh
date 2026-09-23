#!/usr/bin/env bash
# sign-macos.sh signs and notarizes one freshly built goreleaser binary; it
# runs as a build post-hook with the binary path, build target and snapshot
# flag. Linux builds and explicit snapshots pass through unsigned. Darwin
# releases require a prepared keychain and an Accepted notarization result.
# A failure stops the release before GoReleaser archives or publishes it.
set -euo pipefail

binary=$1
target=$2

case "$target" in
  darwin_*) ;;
  *) exit 0 ;;
esac

# GoReleaser snapshots cannot publish, so only they may skip Darwin signing.
if [ "${3:-false}" = "true" ]; then
  exit 0
fi

# Setup sets this after it imports the certificate and notary credentials.
if [ "${APERTURE_SIGNING_READY:-}" != "1" ]; then
  echo "signing keychain not prepared for $target release" >&2
  exit 1
fi

identity="${SIGN_IDENTITY:-Developer ID Application: Tailscale Inc. (W5364U7YZB)}"
profile="${NOTARY_PROFILE:-ci-notary}"

codesign --sign "$identity" --options runtime --timestamp --force "$binary"
codesign --verify --strict --verbose=2 "$binary"

# Bare executables cannot be stapled; Apple serves the ticket by cdhash, so
# the zip only carries the binary to the notary and is never shipped.
submission="$binary.zip"
trap 'rm -f "$submission"' EXIT
zip -j -q "$submission" "$binary"
result=$(xcrun notarytool submit "$submission" \
  --keychain-profile "$profile" --wait --output-format json)
echo "$result"
if ! printf '%s' "$result" | jq -e -s 'length == 1 and (.[0] | type == "object" and .status == "Accepted")' >/dev/null; then
  echo "notarization was not accepted for $target" >&2
  exit 1
fi
