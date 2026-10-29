package clients_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tailscale/aperture-cli/internal/clients"
)

// fakeBinary names a fake client binary the way the platform's executability
// rules require: Windows only treats PATHEXT extensions as executable.
func fakeBinary(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".bat"
	}
	return name
}

func TestFindBinary_PrefersPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", tmp)

	pathBin := filepath.Join(tmp, "pathbin")
	if err := os.MkdirAll(pathBin, 0o755); err != nil {
		t.Fatal(err)
	}
	pathBinary := filepath.Join(pathBin, fakeBinary("opencode"))
	if err := os.WriteFile(pathBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	commonBin := filepath.Join(tmp, ".opencode", "bin")
	if err := os.MkdirAll(commonBin, 0o755); err != nil {
		t.Fatal(err)
	}
	commonBinary := filepath.Join(commonBin, fakeBinary("opencode"))
	if err := os.WriteFile(commonBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", pathBin)

	got := clients.FindBinary("opencode", []string{commonBinary})
	if got != pathBinary {
		t.Errorf("FindBinary() = %q, want %q (PATH should be preferred)", got, pathBinary)
	}
}

func TestFindBinary_FallbackToExtraPaths(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", tmp)

	binDir := filepath.Join(tmp, ".opencode", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(binDir, fakeBinary("opencode"))
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := clients.FindBinary("opencode", []string{fake})
	if got != fake {
		t.Errorf("FindBinary() = %q, want %q", got, fake)
	}
	if !clients.IsInstalled("opencode", []string{fake}) {
		t.Error("IsInstalled() = false, want true")
	}
}

func TestFindBinary_FallbackToCommonBinDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		// FindBinary probes the common dirs for the bare name, which Windows
		// never treats as executable; only LookPath (PATHEXT) finds clients
		// there.
		t.Skip("common bin dirs hold no extension-less executables on Windows")
	}
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	t.Setenv("HOME", tmp)

	localBin := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBinary := filepath.Join(localBin, "claude")
	if err := os.WriteFile(fakeBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := clients.FindBinary("claude", nil)
	if got != fakeBinary {
		t.Errorf("FindBinary() = %q, want %q", got, fakeBinary)
	}
}

func TestFindBinary_NotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", tmp)

	got := clients.FindBinary("claude", nil)
	if got != "" {
		t.Errorf("FindBinary() = %q, want empty", got)
	}
	if clients.IsInstalled("claude", nil) {
		t.Error("IsInstalled() = true, want false")
	}
}

func TestFindBinary_SkipsNonExecutable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", tmp)

	binDir := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nonExec := filepath.Join(binDir, "claude")
	if err := os.WriteFile(nonExec, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := clients.FindBinary("claude", nil)
	if got != "" {
		t.Errorf("FindBinary() = %q, want empty (not executable)", got)
	}
}
