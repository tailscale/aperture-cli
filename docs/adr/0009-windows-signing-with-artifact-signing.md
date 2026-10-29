# 0009. Sign Windows binaries with Azure Artifact Signing from the macOS release job

Status: proposed
Date: 2026-10-29

## Why?

Windows builds shipped unsigned. SmartScreen warns on an unsigned
`aperture.exe` until it earns reputation, and every release resets that
reputation because the hash changes. Managed Windows fleets that block
unsigned executables cannot run it at all, which is the same team-use blocker
ADR 0008 names for the unsigned sidecar.

Release already runs GoReleaser on one macOS runner, and `signtool.exe` does
not run there.

## Decision

1. Windows executables are Authenticode-signed with Azure Artifact Signing
   (formerly Trusted Signing). Certificates are issued to the validated
   Tailscale Inc. identity and live three days, so every signature carries an
   RFC 3161 timestamp from `timestamp.acs.microsoft.com`.
2. Signing is a second GoReleaser build post-hook, `scripts/sign-windows.sh`,
   beside the macOS one. It runs before archiving, so zips and
   `checksums.txt` describe signed bytes. Only snapshots may skip it, and a
   non-snapshot Windows build without readiness fails the release.
3. The signer is Jsign 7.6, pinned by sha256 in `setup-windows-signing.sh`. It
   runs on the existing macOS runner; no Windows build job exists.
4. No long-lived Windows signing secret exists. `azure/login` trades the
   run's OIDC token for an access token that `setup-windows-signing.sh` passes
   to Jsign by environment variable name, never on the command line.
5. The release job runs in the GitHub environment `release`, and the Entra
   federated credential trusts that environment's subject.
6. A `verify-windows` job downloads the published zips on `windows-latest`
   and fails unless `Get-AuthenticodeSignature` reports `Valid`, a timestamp
   and a signer whose subject contains `O=Tailscale Inc.`.

## Consequences

The release job now needs Java and `id-token: write`. A failure of
Azure, the signing quota or the timestamp server fails the whole release,
macOS included, because the hooks share one run. A re-run after the transient
fixes it; an Artifact Signing outage blocks releases until it clears.

`verify-windows` runs after publication. A bad signature is caught after the
zips are public, not before. The gate that keeps unsigned bytes out of the
release is the hook; the job proves the hook's output is what Windows trusts.

Jsign makes one extra signing API call per invocation, and the hook runs once
per binary, so a release spends four calls of quota. Linux builds stay
unsigned. The cask installs them without a signature check, as before.

The signer subject check names the organisation, not the certificate
profile. A second Tailscale profile would also pass.

## Rejected

- **A Windows runner running signtool and the official action.** Needs a
  second job that downloads the zips, signs, re-zips and rewrites
  `checksums.txt` and anything derived from it, after GoReleaser already
  uploaded them. Costs a window where unsigned zips are public.
- **An EV or OV certificate from a CA.** An HSM-bound key in CI means Azure
  Key Vault plus a separate tool, and an OV certificate still builds
  SmartScreen reputation slowly.
- **A service principal client secret.** Expires, must be rotated by hand and
  is a standing credential on a repo that publishes to a Homebrew tap. OIDC
  has nothing to leak.
- **A ref-based federated credential.** Tags differ per release; Entra does
  not wildcard the subject without flexible credentials.
- **Leaving Windows unsigned.** See Why.

## Revisit when

Microsoft changes Artifact Signing eligibility or pricing for Tailscale, the
Linux cask path needs signature checks, a Windows-only build step (an MSI or
an installer) appears, or `verify-windows` flags a bad release often enough
that the check belongs before upload.
