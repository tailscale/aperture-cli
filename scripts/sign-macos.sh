#!/usr/bin/env bash
# sign-macos.sh signs and notarizes one freshly built goreleaser binary; it
# runs as a build post-hook with the binary path and build target as
# arguments. Linux builds and runs without a prepared keychain (PR dry runs,
# local snapshots) pass through unsigned. A failed signature or a notary
# status other than Accepted fails the hook, which fails the goreleaser run
# before anything is archived or published.
set -euo pipefail

binary=$1
target=$2

case "$target" in
  darwin_*) ;;
  *) exit 0 ;;
esac

# Set by scripts/setup-macos-signing.sh once the temporary keychain holds
# the certificate and the notary profile.
if [ "${APERTURE_SIGNING_READY:-}" != "1" ]; then
  echo "signing keychain not prepared, leaving $target unsigned"
  exit 0
fi

identity="${SIGN_IDENTITY:-Developer ID Application: Tailscale Inc. (W5364U7YZB)}"
profile="${NOTARY_PROFILE:-ci-notary}"

codesign --sign "$identity" --options runtime --timestamp --force "$binary"
codesign --verify --strict --verbose=2 "$binary"

# Bare executables cannot be stapled; Apple serves the ticket by cdhash, so
# the zip only carries the binary to the notary and is never shipped.
submission="$binary.zip"
zip -j -q "$submission" "$binary"
result=$(xcrun notarytool submit "$submission" \
  --keychain-profile "$profile" --wait --output-format json || true)
rm -f "$submission"
echo "$result"
if ! printf '%s' "$result" | grep -q '"status":"Accepted"'; then
  echo "notarization was not accepted for $target" >&2
  exit 1
fi
