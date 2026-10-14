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

// $APERTURE_ENDPOINT answers when the tail names no --endpoint: the manual
// client-command parser consumes aperture's own flags itself, bypassing
// cobra's flag/environment merging, so it has to fall back to the
// environment on its own (ADR 0007 env vars). The saved active endpoint is
// seeded unreachable first, so a run that ignored the environment variable
// and fell through to it would fail rather than coincidentally succeed.
func TestHeadlessLaunchUsesEnvironmentEndpoint(t *testing.T) {
	srv := fakeAperture(t)
	binDir := t.TempDir()
	recordDir := installStubPi(t, binDir)
	env := append(hermeticEnv(t, binDir), "APERTURE_E2E_RECORD="+recordDir)

	configDir := apertureConfigDir(t, env)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const unreachable = `{"endpoints":[{"url":"http://127.0.0.1:1"}]}`
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(unreachable), 0o600); err != nil {
		t.Fatal(err)
	}

	env = append(env, "APERTURE_ENDPOINT="+srv.URL)
	if _, stderr, err := run(t, apertureBin, env, "pi"); err != nil {
		t.Fatalf("aperture pi with APERTURE_ENDPOINT set and no flag: %v (%s)", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(recordDir, "argv")); err != nil {
		t.Error("the client did not launch using APERTURE_ENDPOINT")
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

// A non-default settings file is a trust boundary: the launch must say on
// stderr where the configuration came from, where the client is going and
// whether permission prompts are off, because a headless run has no screen
// to show any of it.
func TestHeadlessLaunchWarnsOnConfigOverride(t *testing.T) {
	srv := fakeAperture(t)
	binDir := t.TempDir()
	recordDir := installStubPi(t, binDir)
	env := append(hermeticEnv(t, binDir), "APERTURE_E2E_RECORD="+recordDir)
	settings := filepath.Join(t.TempDir(), "elsewhere.json")

	_, stderr, err := run(t, apertureBin, env, "pi", "--config", settings, "--endpoint", srv.URL)
	if err != nil {
		t.Fatalf("aperture pi with --config: %v (%s)", err, stderr)
	}
	for _, want := range []string{settings, srv.URL, "yolo mode false"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q in the override warning", stderr, want)
		}
	}
	if _, err := os.Stat(filepath.Join(recordDir, "argv")); err != nil {
		t.Error("the client did not launch after the warning")
	}
}
