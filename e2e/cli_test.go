package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The headless command surface, through the built binary: bridges and
// endpoints round-trip, the deprecated single-dash flags warn, and --config
// selects a separate settings file.
func TestCLICommandsRoundTrip(t *testing.T) {
	env := hermeticEnv(t, "")

	stdout, stderr, err := run(t, apertureBin, env, "bridge", "add", "work", "--port", "8991")
	if err != nil {
		t.Fatalf("bridge add: %v (%s)", err, stderr)
	}
	if !strings.Contains(stdout, "work") || !strings.Contains(stdout, "8991") {
		t.Errorf("bridge add printed %q, want the new bridge with its port", stdout)
	}

	if _, stderr, err := run(t, apertureBin, env, "endpoint", "add", "e1", "--bridge", "work", "http://ai"); err != nil {
		t.Fatalf("endpoint add: %v (%s)", err, stderr)
	}

	stdout, _, err = run(t, apertureBin, env, "bridge", "list")
	if err != nil {
		t.Fatalf("bridge list: %v", err)
	}
	if !strings.Contains(stdout, "work") || !strings.Contains(stdout, "8991") {
		t.Errorf("bridge list = %q, want work with its port", stdout)
	}

	stdout, _, err = run(t, apertureBin, env, "endpoint", "list")
	if err != nil {
		t.Fatalf("endpoint list: %v", err)
	}
	if !strings.Contains(stdout, "e1") || !strings.Contains(stdout, "work") {
		t.Errorf("endpoint list = %q, want e1 through work", stdout)
	}

	stdout, _, err = run(t, apertureBin, env, "bridge", "describe", "work")
	if err != nil {
		t.Fatalf("bridge describe: %v", err)
	}
	if !strings.Contains(stdout, "name: work") || !strings.Contains(stdout, "port: 8991") {
		t.Errorf("bridge describe = %q, want the name and port", stdout)
	}

	// The deprecated single-dash long form still parses and warns on stderr.
	stdout, stderr, err = run(t, apertureBin, env, "bridge", "add", "home", "-port", "8992")
	if err != nil {
		t.Fatalf("bridge add with deprecated flag form: %v (%s)", err, stderr)
	}
	if !strings.Contains(stdout, "8992") {
		t.Errorf("bridge add -port printed %q, want the port stored", stdout)
	}
	if !strings.Contains(stderr, "deprecated") {
		t.Errorf("bridge add -port: stderr = %q, want a deprecation warning", stderr)
	}

	stdout, stderr, err = run(t, apertureBin, env, "-version")
	if err != nil {
		t.Fatalf("-version: %v", err)
	}
	if !strings.Contains(stderr, "deprecated") || stdout == "" {
		t.Errorf("-version: stdout=%q stderr=%q, want the version and a deprecation warning", stdout, stderr)
	}

	// A bridge an endpoint uses is refused; deleting the endpoint frees it.
	if _, _, err := run(t, apertureBin, env, "bridge", "delete", "work"); err == nil {
		t.Error("deleted a bridge an endpoint connects through")
	}
	if _, stderr, err := run(t, apertureBin, env, "endpoint", "delete", "e1"); err != nil {
		t.Fatalf("endpoint delete: %v (%s)", err, stderr)
	}
	if _, stderr, err := run(t, apertureBin, env, "bridge", "delete", "work"); err != nil {
		t.Fatalf("bridge delete: %v (%s)", err, stderr)
	}
	stdout, _, _ = run(t, apertureBin, env, "bridge", "list")
	if strings.Contains(stdout, "work") {
		t.Errorf("bridge list = %q after delete, want work gone", stdout)
	}
}

func TestCLIConfigFlagSelectsTheSettingsFile(t *testing.T) {
	env := hermeticEnv(t, "")
	settings := filepath.Join(t.TempDir(), "elsewhere.json")

	if _, stderr, err := run(t, apertureBin, env, "--config", settings, "bridge", "add", "work"); err != nil {
		t.Fatalf("bridge add: %v (%s)", err, stderr)
	}
	stdout, _, err := run(t, apertureBin, env, "--config", settings, "bridge", "list")
	if err != nil {
		t.Fatalf("bridge list: %v", err)
	}
	if !strings.Contains(stdout, "work") {
		t.Errorf("bridge list at the override = %q, want work", stdout)
	}
	stdout, _, err = run(t, apertureBin, env, "bridge", "list")
	if err != nil {
		t.Fatalf("bridge list: %v", err)
	}
	if strings.Contains(stdout, "work") {
		t.Errorf("bridge list at the default = %q, want the override kept separate", stdout)
	}
}

// A failed command logs its error to the run log so details: <path> actually
// has the failure diagnosis when the operator checks it.
func TestFailedCommandLogsToRunLog(t *testing.T) {
	env := hermeticEnv(t, "")
	_, stderr, err := run(t, apertureBin, env, "serve", "--bridge", "ghost")
	if err == nil {
		t.Fatal("serve with missing bridge succeeded")
	}
	if !strings.Contains(stderr, "details:") {
		t.Fatalf("stderr = %q, want details pointing to log file", stderr)
	}

	configDir := apertureConfigDir(t, env)
	logData, err := os.ReadFile(filepath.Join(configDir, "aperture.log"))
	if err != nil {
		t.Fatalf("reading aperture.log: %v", err)
	}
	logStr := string(logData)
	if !strings.Contains(logStr, "command failed") || !strings.Contains(logStr, "ghost") {
		t.Errorf("aperture.log = %q, want error record for failed command", logStr)
	}
}
