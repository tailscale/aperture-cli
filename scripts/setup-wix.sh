#!/usr/bin/env bash
# setup-wix.sh installs the pinned WiX Toolset as a .NET global tool and puts
# it on PATH for later workflow steps. NuGet verifies the package signature on
# install. Requires the .NET SDK, which the Windows runner image carries.
set -euo pipefail

WIX_VERSION=7.0.1

dotnet tool install --global wix --version "$WIX_VERSION"
tools="$HOME/.dotnet/tools"
[ -z "${GITHUB_PATH:-}" ] || echo "$tools" >> "$GITHUB_PATH"
"$tools/wix" --version
