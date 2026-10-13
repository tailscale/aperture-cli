package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The headless launch through the built binary (ADR 0007): `aperture pi`
// connects to the endpoint, resolves provider and model without a menu, and
// passes the tail to the binary verbatim. The alias case (`--search` first,
// unknown to aperture) is the passthrough rule's whole reason to exist.
func TestHeadlessLaunchPi(t *testing.T) {
	srv := fakeAperture(t)
	binDir := t.TempDir()
	recordDir := installStubPi(t, binDir)
	env := append(hermeticEnv(t, binDir), "APERTURE_E2E_RECORD="+recordDir)

	_, stderr, err := run(t, apertureBin, env, "pi", "--endpoint", srv.URL, "--", "--search")
	if err != nil {
		t.Fatalf("aperture pi: %v (%s)", err, stderr)
	}
	argv, err := os.ReadFile(filepath.Join(recordDir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--search") {
		t.Errorf("stub argv = %q, want the passthrough flag in it", argv)
	}
	if !strings.Contains(string(argv), "-e\n") {
		t.Errorf("stub argv = %q, want the extension loaded with -e", argv)
	}

	// The alias case: no aperture flags at all, the client's flag first.
	if _, stderr, err := run(t, apertureBin, env, "pi", "--endpoint", srv.URL, "--search"); err != nil {
		t.Fatalf("aperture pi --search: %v (%s)", err, stderr)
	}
	argv, err = os.ReadFile(filepath.Join(recordDir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--search") {
		t.Errorf("stub argv = %q for the alias case, want --search passed through", argv)
	}

	// The launch recorded LastLaunch, and a named endpoint resolves by name.
	if _, stderr, err := run(t, apertureBin, env, "endpoint", "add", "e1", srv.URL); err != nil {
		t.Fatalf("endpoint add: %v (%s)", err, stderr)
	}
	if _, stderr, err := run(t, apertureBin, env, "pi", "--endpoint", "e1"); err != nil {
		t.Fatalf("aperture pi --endpoint e1: %v (%s)", err, stderr)
	}
}

// A launched client's exit code is aperture's own: an alias for the client
// has to behave like the client.
func TestHeadlessLaunchPropagatesTheExitCode(t *testing.T) {
	srv := fakeAperture(t)
	binDir := t.TempDir()
	stub := filepath.Join(binDir, "pi")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := hermeticEnv(t, binDir)

	_, _, err := run(t, apertureBin, env, "pi", "--endpoint", srv.URL)
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 3 {
		t.Fatalf("exit = %v, want the client's exit code 3", err)
	}
}

// The deprecated single-dash forms belong to aperture only before the client
// command's name: inside the tail they are the client's own flags, passed
// through untouched, so nothing aperture consumes can collide with them.
func TestHeadlessLaunchPassesSingleDashClientFlags(t *testing.T) {
	srv := fakeAperture(t)
	binDir := t.TempDir()
	recordDir := installStubPi(t, binDir)
	env := append(hermeticEnv(t, binDir), "APERTURE_E2E_RECORD="+recordDir)

	if _, stderr, err := run(t, apertureBin, env, "pi", "--endpoint", srv.URL, "-config", "settings.json", "--search"); err != nil {
		t.Fatalf("aperture pi with single-dash client flags: %v (%s)", err, stderr)
	}
	argv, err := os.ReadFile(filepath.Join(recordDir, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-config", "settings.json", "--search"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("stub argv = %q, want %q passed through", argv, want)
		}
	}
}

// --version with a client command after it answers before dispatch; after
// the client's name the flag belongs to the client.
func TestVersionWithAClientCommand(t *testing.T) {
	srv := fakeAperture(t)
	binDir := t.TempDir()
	recordDir := installStubPi(t, binDir)
	env := append(hermeticEnv(t, binDir), "APERTURE_E2E_RECORD="+recordDir)

	stdout, _, err := run(t, apertureBin, env, "--version", "pi", "--endpoint", srv.URL)
	if err != nil {
		t.Fatalf("--version with a client after it: %v", err)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Error("--version printed nothing with a client command after it")
	}
	if _, err := os.Stat(filepath.Join(recordDir, "argv")); !os.IsNotExist(err) {
		t.Error("the client launched despite --version before its name")
	}
}
