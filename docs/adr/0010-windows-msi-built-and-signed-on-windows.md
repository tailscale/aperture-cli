# 0010. Ship a signed WiX MSI per architecture, built on a Windows job

Status: proposed
Date: 2026-11-02

## Why?

Fleets deploy Windows software with `msiexec /i … /qn` and audit it in
Add/Remove Programs. A zip of `aperture.exe` supports neither, so an
enterprise rollout means a hand-written wrapper per customer. The Windows team
builds its installers with WiX, which produces MSIs.

An MSI forces an ordering the macOS release job cannot satisfy. Signing the
MSI does not sign the exe inside it, and a signed MSI cannot be changed, so
the exe has to be signed before WiX packages it. WiX runs only on Windows.

## Decision

1. `installer/aperture.wxs` defines a per-machine MSI that installs
   `aperture.exe` to `Program Files\Aperture CLI` and appends that folder to
   the machine `PATH`. Its `UpgradeCode` is fixed forever; WiX assigns a new
   `ProductCode` per build, so every release is a major upgrade, a rebuild at
   the same version replaces the first, and a downgrade is refused.
2. One MSI per architecture, `aperture_windows_amd64.msi` and
   `aperture_windows_arm64.msi`, because an MSI installs only on the platform
   it names. The names carry no version, matching the zips, so
   `releases/latest/download/…` is a stable URL.
3. Only stable `vX.Y.Z` tags within `255.255.65535` get an MSI;
   `scripts/build-windows-msi.sh` refuses anything else. Prereleases still ship
   zips.
4. A `windows-msi` job runs on `windows-latest` after goreleaser publishes. It
   takes the signed exes from goreleaser as a run artifact, not from the
   release, which anyone with write access can change. It verifies them and the
   published zips, builds both MSIs with WiX
   7.0.1, signs them through the same `sign-windows.sh` hook and key as the
   exe, verifies them, and adds them to the release and `checksums.txt`. It
   replaces ADR 0009's `verify-windows` job.
5. Both release jobs refuse a commit that is not on `main` before anything
   from the checkout runs. The check is inline in the workflow, because a
   dispatched run executes main's workflow but the tag's scripts, and a
   dispatched ref must be a tag. Google Cloud login comes after WiX, so the
   access token never sits beside it.
6. `ci-windows-signing` signs, packages and installs the MSI on native amd64
   and arm64 runners, then upgrades it, reinstalls a rebuild of the same
   version, has a downgrade refused, and uninstalls it, using a throwaway key.

## Consequences

The release now spans two runners, and they publish at different times. If
`windows-msi` fails, the release has zips but no MSIs until someone re-runs
the job. Every step in it is idempotent for that reason.

A second job holds a Google access token, so the HSM can be asked to sign from
two places per release instead of one. It runs in the same `release`
environment under the same provider condition.

WiX 7 is under the Open Source Maintenance Fee. The build passes
`-acceptEula wix7` on the strength of Tailscale's existing sponsorship of
wixtoolset. If that sponsorship lapses, the flag is no longer true.

The main-only check covers dispatched runs and mistaken tags, not a hostile
tag push. A tag push runs the workflow file at that tag, so anyone who can
push tags can push one whose workflow skips the check. The tag ruleset and the
`release` environment's protection rules are what stop them.

A re-run that rebuilds an MSI changes the bytes behind a published version, so
a hash taken from the first build no longer matches.

The MSI appends to the machine `PATH`. Some deployers object to that, and
removing it means they have to add the folder to `PATH` themselves.

## Rejected

- **NSIS.** Builds an `.exe` installer, which has no native `msiexec` silent
  deploy or Group Policy story. It was considered before WiX and set aside for
  the Windows team's toolchain.
- **One MSI with both payloads.** An MSI's platform is a single value in its
  summary stream, so this would still need two packages, or a bundle.
- **Building the MSI on the macOS job.** WiX needs Windows Installer's own
  libraries, so this is not possible.
- **SignTool with Google's CNG provider for the MSI.** Puts a second signing
  mechanism on the runner, which then has to be installed and maintained.
  Jsign already signs MSIs with the same key and settings.
- **A draft release published at the end of `windows-msi`.** Makes the release
  atomic, but the Homebrew cask is pushed from the first job and would point
  at assets that do not download until the draft is published.

## Revisit when

Release tooling gets a packaging step that runs on Windows before
publication, so the MSI can be in place before the release is public. Also
revisit if customers need an installer for one user without admin rights,
which an MSI can only provide through a separate per-user package. And
revisit if the Windows client team's builder can package and sign aperture
for us.
