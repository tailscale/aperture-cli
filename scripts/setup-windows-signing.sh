#!/usr/bin/env bash
# setup-windows-signing.sh prepares a runner for the goreleaser signing hook
# (scripts/sign-windows.sh): it downloads Jsign, which signs Windows
# executables from any OS, and checks it against a pinned digest. Credentials
# are not its business; the workflow hands the hook its key store settings.
#
# Required tools: curl, openssl. Java must be on PATH when the hook runs.
set -euo pipefail

JSIGN_VERSION=7.6
JSIGN_SHA256=6cf58aa080e7ff82656f320cff8d4a3bff30408df8766806dcba3740dd839fa5

# Git Bash on Windows hands over RUNNER_TEMP with backslashes, which neither
# mktemp nor the JVM agree on once the path is spliced into a template.
base="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
umask 077
workdir=$(mktemp -d "${base//\\//}/aperture-signing.XXXXXX")
jar="$workdir/jsign-$JSIGN_VERSION.jar"

# The jar runs with the signing credential in its environment, so it is
# verified against a pinned digest rather than trusted because the release URL
# resolved. openssl is the one digest tool macOS, Linux and Git Bash share.
curl --fail --silent --show-error --location --retry 3 -o "$jar" \
  "https://github.com/ebourg/jsign/releases/download/$JSIGN_VERSION/jsign-$JSIGN_VERSION.jar"
digest=$(openssl dgst -sha256 -r "$jar" | cut -d' ' -f1)
if [ "$digest" != "$JSIGN_SHA256" ]; then
  rm -f "$jar"
  echo "jsign $JSIGN_VERSION does not match the pinned sha256" >&2
  exit 1
fi

echo "Jsign $JSIGN_VERSION: $jar"
if [ -n "${GITHUB_ENV:-}" ]; then
  # Windows releases require readiness; local snapshots may remain unsigned.
  {
    echo "APERTURE_WINDOWS_SIGNING_READY=1"
    echo "JSIGN_JAR=$jar"
  } >> "$GITHUB_ENV"
fi
