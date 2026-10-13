//go:build unix

package e2e

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The local half of serve through the built binary: resolution errors are
// immediate and a SIGTERM stops a running serve cleanly. The data plane is
// out of reach without tailnet credentials, so the route itself is asserted
// in internal/bridges with a fake node.
func TestServeResolutionErrors(t *testing.T) {
	env := hermeticEnv(t, "")

	if _, stderr, err := run(t, apertureBin, env, "serve"); err == nil || !strings.Contains(stderr, "--bridge") {
		t.Errorf("serve without a bridge: err=%v stderr=%q, want --bridge named", err, stderr)
	}

	if _, stderr, err := run(t, apertureBin, env, "serve", "--bridge", "ghost"); err == nil || !strings.Contains(stderr, "ghost") {
		t.Errorf("serve with a missing bridge: err=%v stderr=%q, want the name in the error", err, stderr)
	}

	if _, stderr, err := run(t, apertureBin, env, "bridge", "add", "work"); err != nil {
		t.Fatalf("bridge add: %v (%s)", err, stderr)
	}
	if _, stderr, err := run(t, apertureBin, env, "serve", "--bridge", "work"); err == nil || !strings.Contains(stderr, "port") {
		t.Errorf("serve without a port: err=%v stderr=%q, want the port error", err, stderr)
	}
}

// A serve parked in bridge bring-up (no credentials, so it waits on a login)
// must still stop on SIGTERM, and exit zero: a supervisor restarting on
// non-zero would flap a serve the operator asked to stop.
func TestServeStopsOnSIGTERM(t *testing.T) {
	env := hermeticEnv(t, "")
	if _, stderr, err := run(t, apertureBin, env, "bridge", "add", "work", "--port", "8991"); err != nil {
		t.Fatalf("bridge add: %v (%s)", err, stderr)
	}

	cmd := exec.Command(apertureBin, "serve", "--bridge", "work")
	cmd.Env = env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Wait for the slot lock: the Machine exists, so bring-up is underway
	// and the signal interrupts real work rather than flag parsing.
	configDir := apertureConfigDir(t, env)
	waitForState(t, "the bridge's slot lock", 15*time.Second, func() bool {
		_, ok := slotSuffix(configDir)
		return ok
	})

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve exited %v on SIGTERM, want a clean stop (%s)", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("serve ignored SIGTERM")
	}
}
