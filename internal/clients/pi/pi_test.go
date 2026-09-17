package pi

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/tailscale/aperture-cli/internal/clients/pilike"
)

// The shared behavior is tested in internal/clients/pilike. What is left here
// is only what Pi itself decides.

func TestVariantIdentity(t *testing.T) {
	c := &pilike.Client{V: variant}
	// Name is persisted as LaunchState.LastClientName; changing it
	// invalidates every recorded Pi launch.
	if got := c.Name(); got != "Pi" {
		t.Errorf("Name = %q, want Pi", got)
	}
	if got := c.BinaryName(); got != "pi" {
		t.Errorf("BinaryName = %q, want pi", got)
	}
	if variant.ConfigDir != "pi" {
		t.Errorf("ConfigDir = %q, want pi", variant.ConfigDir)
	}
}

func TestCommonBinaryPaths(t *testing.T) {
	paths := commonBinaryPaths()
	if len(paths) == 0 {
		t.Fatal("commonBinaryPaths is empty")
	}
	// CommonPaths must be full paths to the binary, not directories:
	// FindBinary stats each entry directly.
	for _, p := range paths {
		if filepath.Base(p) != "pi" {
			t.Errorf("CommonPaths entry %q does not end in the binary name", p)
		}
		if !filepath.IsAbs(p) {
			t.Errorf("CommonPaths entry %q is not absolute", p)
		}
	}
}

func TestInstallUninstallCommands(t *testing.T) {
	const wantInstall = "npm install -g --ignore-scripts @earendil-works/pi-coding-agent"
	if variant.InstallCmd != wantInstall {
		t.Errorf("InstallCmd = %q, want %q", variant.InstallCmd, wantInstall)
	}
	wantUninstall := []string{"npm", "uninstall", "-g", "@earendil-works/pi-coding-agent"}
	if !slices.Equal(variant.UninstallArgv, wantUninstall) {
		t.Errorf("UninstallArgv = %v, want %v", variant.UninstallArgv, wantUninstall)
	}
}

// TestNoYoloArgs is the assertion behind the comment on the field. Pi has no
// permission prompts, so nothing here should look like an approval bypass:
// --approve is project-file trust, not tool approval.
func TestNoYoloArgs(t *testing.T) {
	if len(variant.YoloArgs) != 0 {
		t.Errorf("YoloArgs = %v, want none; Pi has no approval flag to skip", variant.YoloArgs)
	}
}
