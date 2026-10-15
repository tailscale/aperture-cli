//go:build unix

package e2e

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A SIGTERM sent to aperture itself must reach the client it launched too:
// aperture's own NotifyContext handler consumes the signal, so nothing
// forwards it to the child unless aperture does so explicitly. A supervisor
// or `kill` on the aperture PID that leaves the child running defeats the
// whole point of sending the signal.
func TestHeadlessLaunchForwardsSIGTERM(t *testing.T) {
	srv := fakeAperture(t)
	binDir := t.TempDir()
	recordDir := installStubPiSleeper(t, binDir)
	env := append(hermeticEnv(t, binDir), "APERTURE_E2E_RECORD="+recordDir)

	cmd := exec.Command(apertureBin, "pi", "--endpoint", srv.URL)
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	waitForFile(t, filepath.Join(recordDir, "ready"))

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("aperture pi exited 0 on SIGTERM to a still-sleeping child, want a signal-killed error")
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("aperture pi did not stop on SIGTERM; the child kept running")
	}
}
