# verify-windows-signature.ps1 asks Windows itself whether each executable's
# Authenticode signature is valid, timestamped and issued to the expected
# signer. Linux and macOS tools cannot answer this: the trust store that
# matters is Windows'. Used by the release workflow against published zips and
# by the signing smoke test against a self-signed build.
#
#   -Path           executables to check
#   -SignerPattern  regex the signer certificate subject must match
param(
  [Parameter(Mandatory)][string[]]$Path,
  [Parameter(Mandatory)][string]$SignerPattern
)
$ErrorActionPreference = 'Stop'

foreach ($file in $Path) {
  $sig = Get-AuthenticodeSignature -FilePath $file
  "${file}: $($sig.Status) $($sig.SignerCertificate.Subject)"
  if ($sig.Status -ne 'Valid') { throw "${file}: signature is $($sig.Status): $($sig.StatusMessage)" }
  if ($sig.SignerCertificate.Subject -notmatch $SignerPattern) { throw "${file}: unexpected signer $($sig.SignerCertificate.Subject)" }
  if ($null -eq $sig.TimeStamperCertificate) { throw "${file}: no timestamp" }
}
