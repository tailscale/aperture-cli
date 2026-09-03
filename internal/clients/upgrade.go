package clients

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InstallMethod identifies which package manager owns a client binary
type InstallMethod int

const (
	MethodNative   InstallMethod = iota // client's own installer
	MethodNPM                           //global npm package
	MethodHomebrew                      //Homebrew formula or cask.
)

// UpgradeSpec describes the upgrade paths a client supports
type UpgradeSpec struct {
	// locate the installed binary
	BinaryName           string
	ExtraPaths           []string
	NPMPackage           string   // global npm package name
	BrewName             string   // Homebrew formula/cask name
	SelfUpdateArgs       []string // invoke the binary's built-in updater
	NativeUpgradeCommand string   // re-runs the client's official standalone installer
}

// PlanUpgrade builds an UpgradePlan client whose command matches how the binary was installed
func PlanUpgrade(s UpgradeSpec) UpgradePlan {
	bin := FindBinary(s.BinaryName, s.ExtraPaths)
	if bin == "" {
		return UpgradePlan{
			Hint: s.BinaryName + " is not installed",
			Run: func() (*exec.Cmd, error) {
				return nil, fmt.Errorf("%s binary not found", s.BinaryName)
			},
		}
	}
	method := DetectInstallMethod(bin, s.NPMPackage)
	if method == MethodHomebrew {
		// prefer the installed package name
		if name := brewPackageName(resolvePath(bin)); name != "" {
			s.BrewName = name
		}
	}
	hint := upgradeCommand(s, s.BinaryName, method)
	cmd := upgradeCommand(s, bin, method)
	if cmd == "" {
		return UpgradePlan{Hint: "No known upgrade path for " + bin}
	}
	cmd += " && " + shellQuote(bin) + " --version"
	return UpgradePlan{
		Hint: hint,
		Run: func() (*exec.Cmd, error) {
			return exec.Command("/bin/sh", "-c", cmd), nil
		},
	}
}

// upgradeCommand returns the upgrade command for the detected install method
func upgradeCommand(s UpgradeSpec, bin string, m InstallMethod) string {
	switch m {
	case MethodNPM:
		if s.NPMPackage != "" {
			return "npm install -g " + s.NPMPackage + "@latest"
		}
	case MethodHomebrew:
		if s.BrewName != "" {
			return "brew upgrade " + s.BrewName
		}
	}
	if len(s.SelfUpdateArgs) > 0 {
		return shellQuote(bin) + " " + strings.Join(s.SelfUpdateArgs, " ")
	}
	if s.NativeUpgradeCommand != "" {
		return s.NativeUpgradeCommand
	}
	if s.NPMPackage != "" {
		return "npm install -g " + s.NPMPackage + "@latest"
	}
	return ""
}

// DetectInstallMethod inspects a binary's on-disk location to determine which package manager owns it
func DetectInstallMethod(bin, npmPkg string) InstallMethod {
	resolved := resolvePath(bin)
	// check npm first; with a Homebrew-installed node, npm's global
	// prefix also lives under the Homebrew prefix.
	if npmOwns(bin, resolved, npmPkg) {
		return MethodNPM
	}
	if brewOwns(resolved) {
		return MethodHomebrew
	}
	return MethodNative
}

func resolvePath(bin string) string {
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		return r
	}
	return bin
}

// brewPackageName extracts the formula/cask name from a resolved Cellar or Caskroom path
func brewPackageName(resolved string) string {
	segs := strings.Split(filepath.ToSlash(resolved), "/")
	for i, seg := range segs {
		if (seg == "Cellar" || seg == "Caskroom") && i+1 < len(segs) {
			return segs[i+1]
		}
	}
	return ""
}

func npmOwns(bin, resolved, pkg string) bool {
	if strings.Contains(resolved, "node_modules") {
		return true
	}
	if pkg == "" {
		return false
	}
	dir := filepath.Dir(bin)
	for _, cand := range []string{
		filepath.Join(dir, "node_modules", pkg),              // Windows npm prefix
		filepath.Join(dir, "..", "lib", "node_modules", pkg), // Unix npm prefix
	} {
		if _, err := os.Stat(cand); err == nil {
			return true
		}
	}
	return false
}

func brewOwns(resolved string) bool {
	for _, marker := range []string{"/Cellar/", "/Caskroom/", "/homebrew/", "/linuxbrew/"} {
		if strings.Contains(resolved, marker) {
			return true
		}
	}
	return false
}

// shellQuote single-quotes s for /bin/sh when it contains characters that
// would be interpreted by the shell.
func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t'\"$`\\&|;<>(){}*?#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
