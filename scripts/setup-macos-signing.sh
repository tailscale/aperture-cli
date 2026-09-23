#!/usr/bin/env bash
# setup-macos-signing.sh prepares a GitHub macOS runner for the goreleaser
# signing hook (scripts/sign-macos.sh): it imports the Developer ID
# Application certificate from APPLE_CERT_P12 into a fresh temporary
# keychain, puts that keychain on the user search list so codesign sees it,
# and stores the notarization credentials under NOTARY_PROFILE in the login
# keychain, which is where `notarytool --keychain-profile` looks.
#
# Required environment:
#   APPLE_CERT_P12       base64-encoded .p12 of the certificate and private key
#   APPLE_CERT_PASSWORD  password of that .p12
#   APPLE_ID             Apple ID used for notarization
#   APPLE_ID_PASSWORD    app-specific password for that Apple ID
set -euo pipefail

: "${APPLE_CERT_P12:?set to the base64-encoded Developer ID Application .p12}"
: "${APPLE_CERT_PASSWORD:?set to the .p12 password}"
: "${APPLE_ID:?set to the notarization Apple ID}"
: "${APPLE_ID_PASSWORD:?set to its app-specific password}"

# Printed on every Developer ID signature; an identifier, not a secret.
TEAM_ID=W5364U7YZB
NOTARY_PROFILE="${NOTARY_PROFILE:-ci-notary}"

umask 077
workdir=$(mktemp -d "${TMPDIR:-/tmp}/aperture-signing.XXXXXX")
keychain="$workdir/signing.keychain-db"
keychain_password=$(openssl rand -base64 32)

printf '%s' "$APPLE_CERT_P12" | base64 -d > "$workdir/certificate.p12"
security create-keychain -p "$keychain_password" "$keychain"
security set-keychain-settings -lut 3600 "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
security import "$workdir/certificate.p12" -k "$keychain" \
  -P "$APPLE_CERT_PASSWORD" -T /usr/bin/codesign
rm "$workdir/certificate.p12"
# Without this, codesign prompts for the keychain password on first use and
# the headless runner hangs.
security set-key-partition-list -S apple-tool:,apple:,codesign: \
  -s -k "$keychain_password" "$keychain"
# The Makefile's codesign and find-identity calls take no --keychain flag, so
# the temporary keychain has to sit on the user search list.
# shellcheck disable=SC2046
security list-keychains -d user \
  -s "$keychain" $(security list-keychains -d user | tr -d '"')

xcrun notarytool store-credentials "$NOTARY_PROFILE" \
  --apple-id "$APPLE_ID" --password "$APPLE_ID_PASSWORD" --team-id "$TEAM_ID"

echo "Signing identity: Developer ID Application: Tailscale Inc. ($TEAM_ID)"
echo "Notary profile: $NOTARY_PROFILE"
if [ -n "${GITHUB_ENV:-}" ]; then
  echo "SIGNING_KEYCHAIN=$keychain" >> "$GITHUB_ENV"
  # The goreleaser hook signs only when this is set, so PR dry runs and
  # local snapshots pass binaries through unsigned.
  echo "APERTURE_SIGNING_READY=1" >> "$GITHUB_ENV"
fi
