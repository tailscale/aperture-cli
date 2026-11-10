package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Only a stable tag inside the MSI version range may package. These fail
// before WiX runs, so they need no WiX and run on every platform.
func TestBuildWindowsMSIRejects(t *testing.T) {
	if _, err := os.ReadFile("build-windows-msi.sh"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "aperture.exe")
	if err := os.WriteFile(exe, []byte("signed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, tag, arch, exe, reason string
	}{
		{name: "prerelease", tag: "v1.2.3-rc.1", reason: "not a stable"},
		{name: "build metadata", tag: "v1.2.3+abc", reason: "not a stable"},
		{name: "no v prefix", tag: "1.2.3", reason: "not a stable"},
		{name: "two fields", tag: "v1.2", reason: "not a stable"},
		{name: "major over 255", tag: "v256.0.0", reason: "beyond the MSI version range"},
		{name: "minor over 255", tag: "v1.256.0", reason: "beyond the MSI version range"},
		{name: "build over 65535", tag: "v1.0.65536", reason: "beyond the MSI version range"},
		{name: "unsupported arch", tag: "v1.2.3", arch: "386", reason: "unsupported architecture"},
		{name: "missing exe", tag: "v1.2.3", exe: filepath.Join(dir, "absent.exe"), reason: "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			arch, src := tt.arch, tt.exe
			if arch == "" {
				arch = "amd64"
			}
			if src == "" {
				src = exe
			}
			out := filepath.Join(t.TempDir(), "aperture.msi")
			output, err := exec.Command("bash", "build-windows-msi.sh", src, tt.tag, arch, out).CombinedOutput()
			if err == nil {
				t.Fatalf("built an MSI for tag %s arch %s\n%s", tt.tag, arch, output)
			}
			if !strings.Contains(string(output), tt.reason) {
				t.Errorf("failed for another reason, want %q:\n%s", tt.reason, output)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("a rejected build left %s behind", out)
			}
		})
	}
}

// A real WiX build: the MSI must carry the tag's version and the platform of
// the architecture it was asked for, since Windows Installer refuses an MSI
// whose platform does not match the machine. Windows CI installs WiX; other
// platforms skip.
func TestBuildWindowsMSI(t *testing.T) {
	if _, err := exec.LookPath("wix"); err != nil {
		t.Skip("wix not installed; scripts/setup-wix.sh installs it on Windows")
	}
	tests := []struct {
		tag, arch, wantVersion, wantPlatform string
	}{
		{tag: "v1.2.3", arch: "amd64", wantVersion: "1.2.3", wantPlatform: "x64"},
		{tag: "v01.02.09", arch: "arm64", wantVersion: "1.2.9", wantPlatform: "Arm64"},
	}
	for _, tt := range tests {
		t.Run(tt.arch, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "aperture.exe")
			if err := os.WriteFile(exe, []byte("payload\n"), 0600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "aperture.msi")
			if output, err := exec.Command("bash", "build-windows-msi.sh", exe, tt.tag, tt.arch, out).CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, output)
			}
			// Property 7 of the summary stream is the Template, "<platform>;<languages>".
			query := `$i = New-Object -ComObject WindowsInstaller.Installer
$db = $i.OpenDatabase($env:MSI, 0)
$v = $db.OpenView("SELECT Value FROM Property WHERE Property='ProductVersion'")
$v.Execute(); $r = $v.Fetch()
"$($r.StringData(1)) $($db.SummaryInformation(0).Property(7))"`
			cmd := exec.Command("powershell", "-NoProfile", "-Command", query)
			cmd.Env = append(os.Environ(), "MSI="+out)
			got, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("read MSI: %v\n%s", err, got)
			}
			fields := strings.Fields(string(got))
			if len(fields) != 2 {
				t.Fatalf("unexpected MSI properties %q", got)
			}
			if fields[0] != tt.wantVersion {
				t.Errorf("ProductVersion = %s, want %s", fields[0], tt.wantVersion)
			}
			if platform, _, _ := strings.Cut(fields[1], ";"); !strings.EqualFold(platform, tt.wantPlatform) {
				t.Errorf("platform = %s, want %s", platform, tt.wantPlatform)
			}
		})
	}
}

// UpgradeCode must never change: a new one makes the next MSI install beside
// the old version instead of upgrading it.
func TestInstallerUpgradeCodeIsStable(t *testing.T) {
	wxs, err := os.ReadFile(filepath.Join("..", "installer", "aperture.wxs"))
	if err != nil {
		t.Fatal(err)
	}
	const upgradeCode = `UpgradeCode="f5f4e380-4076-4b6e-ac1b-af4fc89efc43"`
	if !strings.Contains(string(wxs), upgradeCode) {
		t.Errorf("installer/aperture.wxs lost %s; shipped MSIs upgrade only under that code", upgradeCode)
	}
}
