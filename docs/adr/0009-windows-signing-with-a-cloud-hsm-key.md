# 0009. Sign Windows binaries with an aperture-only Cloud HSM key

Status: proposed
Date: 2026-10-29

## Why?

Windows builds shipped unsigned. SmartScreen warns on an unsigned
`aperture.exe`, and every release starts from zero reputation because the hash
changes. Managed Windows fleets that block unsigned executables cannot run it
at all, which is the same team-use blocker ADR 0008 names for the unsigned
sidecar.

The Windows client team does not want signing key material in GitHub Actions,
and the client's own signing key is a production identity. Release already
runs GoReleaser on one macOS runner, and `signtool.exe` does not run there.

## Decision

1. Windows executables are Authenticode-signed with a key that never leaves a
   Google Cloud HSM. It is an aperture-only key, in its own key ring, and is
   never the client's `codesigning` key. A public CA issues the certificate
   against it.
2. Signing is a GoReleaser build post-hook, `scripts/sign-windows.sh`, beside
   the macOS one. It runs before archiving, so zips and `checksums.txt`
   describe signed bytes. Only snapshots may skip it, and a non-snapshot
   Windows build without readiness fails the release.
3. The signer is Jsign 7.6, pinned by sha256 in `setup-windows-signing.sh`. It
   runs on the existing macOS runner, so no Windows job sits inside the
   release. Every signature carries an RFC 3161 timestamp.
4. The key store is configuration. The hook reads `WINDOWS_SIGN_STORETYPE`,
   `_KEYSTORE`, `_ALIAS`, `_CERTFILE` and `_STOREPASS`, and knows nothing
   about Google. The release sets `GOOGLECLOUD`; the `ci-windows-signing`
   workflow runs the same hook with `PKCS12` and a throwaway self-signed key
   from `make-selfsigned-codesign.sh`, so the path is proved before the HSM
   key exists.
5. The runner holds no long-lived credential. Workload identity federation
   trades the run's OIDC token for an access token whose service account has
   `roles/cloudkms.signer` on that one key. The provider trusts only this
   repository's `release` environment.
6. `verify-windows` downloads the published zips on `windows-latest` and runs
   `scripts/verify-windows-signature.ps1`: `Get-AuthenticodeSignature` must say
   `Valid`, with a timestamp and a signer subject containing `O=Tailscale Inc.`.
   The same script judges the self-signed CI build.

## Consequences

No key or certificate-with-key sits in Actions, but the runner can still ask
the HSM to sign while the job runs. The `release` environment restriction and
the signer role on a single key are the controls, and both are IAM, not code.
A compromised release job can sign aperture-shaped binaries as Tailscale Inc.
until its token expires.

The release job now needs Java and `id-token: write`. A failure of Google
Cloud, the signing key or the timestamp server fails the whole release, macOS
included, because the hooks share one run.

`verify-windows` runs after publication, so a bad signature is caught after
the zips are public. The hook is what keeps unsigned bytes out; the job proves
that what it produced is what Windows trusts.

The CA certificate has to be bought, validated and renewed, unlike a
short-lived certificate from a managed service. Jsign supports only EC and RSA
PKCS#1 v1.5 keys, which rules out the RSA-PSS padding that blocked the
osslsigncode attempt. The CI smoke test uses an EC P-256 key, so a green run
shows Windows accepts that class of key; the CA must accept it too, or the key
is recreated as RSA.

Linux builds stay unsigned. The cask installs them without a signature check,
as before.

## Rejected

- **Azure Artifact Signing.** Short-lived certificates and no purchase, with
  OIDC auth from Actions. Rejected by the choice of existing infrastructure,
  not on its merits; it is the fallback if the CA order stalls, and the hook's
  storetype indirection means switching is configuration plus a different
  login step.
- **SignTool with the Cloud KMS CNG provider.** Needs a Windows machine, which
  puts a Windows job inside the release or signs after publication, leaving
  unsigned zips public. It also gains nothing over Jsign on the same key.
- **The client's signing key or certificate in Actions.** The reason this
  decision exists.
- **A self-signed certificate in releases.** Windows treats it as unsigned,
  with a worse publisher prompt.

## Revisit when

The CA will not issue against an EC key, Google Cloud signing latency or
quota fails releases, the client team's builder can take over signing for
aperture, or a Windows-only artifact (an MSI) appears that needs a Windows
step in the release.
