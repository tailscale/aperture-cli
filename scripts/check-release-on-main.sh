#!/usr/bin/env bash
# check-release-on-main.sh fails unless the checked-out commit is on main, so a
# tag pushed from a branch never reaches a signing key. It checks HEAD, not
# GITHUB_SHA: a workflow_dispatch run checks out the requested tag while
# GITHUB_SHA is still the branch it was dispatched from.
#
# This stops mistakes, not attackers. A tag's workflow is the file at that tag,
# so anyone who can push a tag can push one without this step; the tag ruleset
# and the release environment are what keep them out.
set -euo pipefail

git fetch --no-tags --quiet origin main
if ! git merge-base --is-ancestor HEAD origin/main; then
  echo "::error::$(git rev-parse --short HEAD) is not on main; refusing to sign a release from it"
  exit 1
fi
