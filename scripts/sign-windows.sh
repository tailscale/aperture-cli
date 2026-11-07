#!/usr/bin/env bash
# sign-windows.sh Authenticode-signs one freshly built goreleaser binary with
# Jsign; it runs as a build post-hook with the binary path, build target and
# snapshot flag. Non-Windows builds and explicit snapshots pass through
# unsigned. Windows releases require the setup script to have run and a
# timestamped signature. A failure stops the release before GoReleaser
# archives or publishes it.
#
# The key store is configuration, not code, so swapping where the key lives
# touches only the environment, never this script:
#
#   WINDOWS_SIGN_STORETYPE  Jsign --storetype: PKCS12 (CI smoke test with a
#                           self-signed key), GOOGLECLOUD (release)
#   WINDOWS_SIGN_KEYSTORE   PKCS12: the .p12 path. GOOGLECLOUD: the key ring,
#                           projects/P/locations/L/keyRings/R
#   WINDOWS_SIGN_STOREPASS  PKCS12: the .p12 password. GOOGLECLOUD: an access
#                           token. Read from the environment by Jsign; it
#                           never appears on a command line.
#   WINDOWS_SIGN_ALIAS      optional. GOOGLECLOUD: <key>/cryptoKeyVersions/N,
#                           pinned so signing needs no list permission.
#   WINDOWS_SIGN_CERTFILE   optional. GOOGLECLOUD: the CA's PEM chain, since
#                           the HSM holds only the private key.
#   JSIGN_JAR               set by scripts/setup-windows-signing.sh
set -euo pipefail

binary=$1
target=$2

case "$target" in
  windows_*) ;;
  *) exit 0 ;;
esac

# GoReleaser snapshots cannot publish, so only they may skip Windows signing.
if [ "${3:-false}" = "true" ]; then
  exit 0
fi

# Setup sets this after it fetched and verified Jsign.
if [ "${APERTURE_WINDOWS_SIGNING_READY:-}" != "1" ]; then
  echo "signing not prepared for $target release" >&2
  exit 1
fi

storetype="${WINDOWS_SIGN_STORETYPE:?set the Jsign storetype}"
keystore="${WINDOWS_SIGN_KEYSTORE:?set the key store}"
jar="${JSIGN_JAR:?setup must export the Jsign jar}"
: "${WINDOWS_SIGN_STOREPASS:?set the key store password or access token}"

args=(--storetype "$storetype" --keystore "$keystore" --storepass env:WINDOWS_SIGN_STOREPASS)
[ -z "${WINDOWS_SIGN_ALIAS:-}" ] || args+=(--alias "$WINDOWS_SIGN_ALIAS")
if [ -n "${WINDOWS_SIGN_CERTFILE:-}" ]; then
  [ -f "$WINDOWS_SIGN_CERTFILE" ] || { echo "certificate chain $WINDOWS_SIGN_CERTFILE not found" >&2; exit 1; }
  args+=(--certfile "$WINDOWS_SIGN_CERTFILE")
fi

# A certificate expires; the RFC 3161 timestamp is what keeps the signature
# valid after it does, so a signature without one is a failure, not a default.
java -jar "$jar" "${args[@]}" \
  --alg SHA-256 \
  --tsmode RFC3161 \
  --tsaurl http://timestamp.digicert.com \
  --name aperture \
  "$binary"
