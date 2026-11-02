# test-windows-msi-install.ps1 installs, upgrades, refuses a downgrade of and
# uninstalls aperture's MSI on this machine, checking what an administrator
# deploying it sees: one Add/Remove Programs entry with the right publisher and
# version, aperture on the machine PATH, and nothing left behind. It changes
# the machine, so it is for a throwaway runner or VM, run as administrator.
#
#   -Old, -New                MSIs of the same architecture, -New the higher version
#   -OldVersion, -NewVersion  the versions they carry, e.g. 0.0.1
param(
  [Parameter(Mandatory)][string]$Old,
  [Parameter(Mandatory)][string]$New,
  [Parameter(Mandatory)][string]$OldVersion,
  [Parameter(Mandatory)][string]$NewVersion
)
$ErrorActionPreference = 'Stop'
# msiexec wants a Windows path, not the forward slashes a workflow hands over.
$Old = (Resolve-Path $Old).Path
$New = (Resolve-Path $New).Path
$installDir = Join-Path $env:ProgramFiles 'Aperture CLI'

function Invoke-Msiexec([string[]]$Arguments) {
  (Start-Process msiexec.exe -ArgumentList ($Arguments + '/qn', '/norestart') -Wait -PassThru).ExitCode
}

function Assert-Installed([string]$Version) {
  $entries = @(Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*' |
    Where-Object DisplayName -eq 'Aperture CLI')
  if ($entries.Count -ne 1) { throw "want one Add/Remove Programs entry, found $($entries.Count)" }
  if ($entries[0].Publisher -ne 'Tailscale Inc.') { throw "publisher is $($entries[0].Publisher)" }
  if ($entries[0].DisplayVersion -ne $Version) { throw "version is $($entries[0].DisplayVersion), want $Version" }

  # A fresh shell builds PATH from the registry; this process's copy is stale.
  $env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine')
  $found = (Get-Command aperture -ErrorAction SilentlyContinue).Source
  if ($found -ne (Join-Path $installDir 'aperture.exe')) { throw "aperture on PATH is '$found'" }
  & aperture --version
  if ($LASTEXITCODE -ne 0) { throw "aperture --version exited $LASTEXITCODE" }
  "installed $Version at $found"
}

$code = Invoke-Msiexec '/i', $Old
if ($code -ne 0) { throw "install of $Old exited $code" }
Assert-Installed $OldVersion

$code = Invoke-Msiexec '/i', $New
if ($code -ne 0) { throw "upgrade to $New exited $code" }
Assert-Installed $NewVersion

# MajorUpgrade blocks a downgrade with a fatal error, 1603.
$code = Invoke-Msiexec '/i', $Old
if ($code -ne 1603) { throw "downgrade to $Old exited $code, want 1603" }
Assert-Installed $NewVersion

$code = Invoke-Msiexec '/x', $New
if ($code -ne 0) { throw "uninstall exited $code" }
if (Test-Path $installDir) { throw "$installDir is left behind" }
if ([Environment]::GetEnvironmentVariable('Path', 'Machine') -like "*$installDir*") { throw "PATH still names $installDir" }
"uninstalled cleanly"
