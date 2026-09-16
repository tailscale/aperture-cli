package omp

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/tailscale/aperture-cli/internal/clients/pilike"
)

// The shared behavior is tested in internal/clients/pilike. What is left here
// is only what Oh My Pi itself decides.

func TestVariantIdentity(t *testing.T) {
	c := &pilike.Client{V: variant}
	// Name is persisted as LaunchState.LastClientName; changing it
	// invalidates every recorded Oh My Pi launch.
	if got := c.Name(); got != "Oh My Pi" {
		t.Errorf("Name = %q, want Oh My Pi", got)
	}
	if got := c.BinaryName(); got != "omp" {
		t.Errorf("BinaryName = %q, want omp", got)
	}
	if variant.ConfigDir != "omp" {
		t.Errorf("ConfigDir = %q, want omp", variant.ConfigDir)
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
		if filepath.Base(p) != "omp" {
			t.Errorf("CommonPaths entry %q does not end in the binary name", p)
		}
		if !filepath.IsAbs(p) {
			t.Errorf("CommonPaths entry %q is not absolute", p)
		}
	}
}

func TestInstallUninstallCommands(t *testing.T) {
	const wantInstall = "bun install -g @oh-my-pi/pi-coding-agent"
	if variant.InstallCmd != wantInstall {
		t.Errorf("InstallCmd = %q, want %q", variant.InstallCmd, wantInstall)
	}
	wantUninstall := []string{"bun", "uninstall", "-g", "@oh-my-pi/pi-coding-agent"}
	if !slices.Equal(variant.UninstallArgv, wantUninstall) {
		t.Errorf("UninstallArgv = %v, want %v", variant.UninstallArgv, wantUninstall)
	}
}

// TestYoloArgs pins the one place OMP diverges from Pi on permissions.
// --auto-approve is a real approval bypass here, unlike Pi's --approve.
func TestYoloArgs(t *testing.T) {
	if want := []string{"--auto-approve"}; !slices.Equal(variant.YoloArgs, want) {
		t.Errorf("YoloArgs = %v, want %v", variant.YoloArgs, want)
	}
}
