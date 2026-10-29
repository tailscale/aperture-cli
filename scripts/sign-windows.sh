#!/usr/bin/env bash
# sign-windows.sh Authenticode-signs one freshly built goreleaser binary with
# Azure Artifact Signing; it runs as a build post-hook with the binary path,
# build target and snapshot flag. Non-Windows builds and explicit snapshots
# pass through unsigned. Windows releases require the setup script to have run
# and a signature that Artifact Signing issued and timestamped. A failure stops
# the release before GoReleaser archives or publishes it.
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

# Setup sets this after it fetched Jsign and the access token.
if [ "${APERTURE_WINDOWS_SIGNING_READY:-}" != "1" ]; then
  echo "signing not prepared for $target release" >&2
  exit 1
fi

endpoint="${ARTIFACT_SIGNING_ENDPOINT:?release job must set the signing endpoint}"
account="${ARTIFACT_SIGNING_ACCOUNT:?release job must set the signing account}"
profile="${ARTIFACT_SIGNING_PROFILE:?release job must set the certificate profile}"
jar="${JSIGN_JAR:?setup must export the Jsign jar}"
: "${ARTIFACT_SIGNING_TOKEN:?setup must export the access token}"

# The token goes by environment variable name, never on the command line where
# ps shows it. Artifact Signing certificates live three days, so the RFC 3161
# timestamp is what keeps the signature valid after they expire.
java -jar "$jar" \
  --storetype TRUSTEDSIGNING \
  --keystore "$endpoint" \
  --alias "$account/$profile" \
  --storepass env:ARTIFACT_SIGNING_TOKEN \
  --alg SHA-256 \
  --tsmode RFC3161 \
  --tsaurl http://timestamp.acs.microsoft.com \
  --name aperture \
  "$binary"
