#!/usr/bin/env bash
# setup-windows-signing.sh prepares a GitHub runner for the goreleaser signing
# hook (scripts/sign-windows.sh): it downloads a pinned Jsign, which signs
# Windows executables from any OS, and exchanges the runner's Azure login for
# a short-lived Artifact Signing access token. Run it after azure/login; no
# long-lived Windows signing secret exists anywhere.
#
# Required environment:
#   ARTIFACT_SIGNING_ENDPOINT  regional endpoint host, e.g. weu.codesigning.azure.net
#   ARTIFACT_SIGNING_ACCOUNT   Artifact Signing account name
#   ARTIFACT_SIGNING_PROFILE   certificate profile name
# Required tools: az (logged in), curl, shasum.
set -euo pipefail

: "${ARTIFACT_SIGNING_ENDPOINT:?set to the Artifact Signing endpoint host}"
: "${ARTIFACT_SIGNING_ACCOUNT:?set to the Artifact Signing account name}"
: "${ARTIFACT_SIGNING_PROFILE:?set to the certificate profile name}"

JSIGN_VERSION=7.6
JSIGN_SHA256=6cf58aa080e7ff82656f320cff8d4a3bff30408df8766806dcba3740dd839fa5

umask 077
workdir=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/aperture-signing.XXXXXX")
jar="$workdir/jsign-$JSIGN_VERSION.jar"

# The jar runs with the signing token in its environment, so it is verified
# against a pinned digest rather than trusted because the release URL resolved.
curl --fail --silent --show-error --location --retry 3 -o "$jar" \
  "https://github.com/ebourg/jsign/releases/download/$JSIGN_VERSION/jsign-$JSIGN_VERSION.jar"
if ! printf '%s  %s\n' "$JSIGN_SHA256" "$jar" | shasum -a 256 -c - >/dev/null; then
  rm -f "$jar"
  echo "jsign $JSIGN_VERSION does not match the pinned sha256" >&2
  exit 1
fi

token=$(az account get-access-token --resource https://codesigning.azure.net \
  --query accessToken --output tsv)
if [ -z "$token" ]; then
  echo "az returned no Artifact Signing access token" >&2
  exit 1
fi
# Masked before it can reach any log line, including the GITHUB_ENV write.
echo "::add-mask::$token"

echo "Signing endpoint: $ARTIFACT_SIGNING_ENDPOINT"
echo "Signing profile: $ARTIFACT_SIGNING_ACCOUNT/$ARTIFACT_SIGNING_PROFILE"
if [ -n "${GITHUB_ENV:-}" ]; then
  # Windows releases require readiness; local snapshots may remain unsigned.
  {
    echo "APERTURE_WINDOWS_SIGNING_READY=1"
    echo "JSIGN_JAR=$jar"
    echo "ARTIFACT_SIGNING_TOKEN=$token"
  } >> "$GITHUB_ENV"
fi
