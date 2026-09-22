// Package e2e exercises the built aperture binary end to end: real process,
// real PTY, a fake Aperture over HTTP, and stub agent binaries on PATH.
// Nothing here touches the user's real home, config, or tailnet.
package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// terminal is a running aperture process attached to a PTY, with the output
// stream captured for assertions.
type terminal struct {
	cmd  *exec.Cmd
	ptmx *os.File

	mu  sync.Mutex
	out bytes.Buffer
}

// spawn starts bin on a PTY with args and env and begins capturing output.
func spawn(t *testing.T, bin string, args []string, env []string) *terminal {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatalf("spawning %s: %v", bin, err)
	}
	term := &terminal{cmd: cmd, ptmx: ptmx}
	t.Cleanup(term.kill)
	go func() {
		var buf [4096]byte
		for {
			n, err := ptmx.Read(buf[:])
			if n > 0 {
				term.mu.Lock()
				term.out.Write(buf[:n])
				term.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return term
}

// kill ends the process if it has not already exited. Terminals parked on a
// screen that never exits (a bridge waiting on login) are reaped here; in
// suites that end in waitExit every call is a no-op.
func (term *terminal) kill() {
	if term.cmd.Process == nil {
		return
	}
	_ = term.cmd.Process.Kill()
	_ = term.cmd.Wait()
	_ = term.ptmx.Close()
}

// send types keys into the terminal.
func (term *terminal) send(keys string) {
	if _, err := term.ptmx.WriteString(keys); err != nil {
		// The process may have exited between the last waitFor and this
		// send; waitExit reports the real outcome.
		return
	}
}

// screen is everything the process has printed so far, escape sequences
// stripped, so assertions match words rather than terminal control bytes.
func (term *terminal) screen() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return ansi.Strip(term.out.String())
}

// waitFor blocks until substr appears on screen, failing with the full
// screen after a generous timeout. The timeout covers bridge bring-up on a
// slow CI box; locally every screen arrives in milliseconds.
func (term *terminal) waitFor(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(term.screen(), substr) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; screen:\n%s", substr, term.screen())
}

// waitExit expects the process to exit 0 within the timeout.
func (term *terminal) waitExit(t *testing.T) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- term.cmd.Wait() }()
	select {
	case err := <-done:
		term.ptmx.Close()
		if err != nil {
			t.Fatalf("exit: %v; screen:\n%s", err, term.screen())
		}
	case <-time.After(15 * time.Second):
		_ = term.cmd.Process.Kill()
		t.Fatalf("process still running; screen:\n%s", term.screen())
	}
}

// run executes bin without a PTY and returns its streams. For the flag
// paths that resolve before the TUI takes the terminal.
func run(t *testing.T, bin string, env []string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// hermeticEnv builds the environment for one run: a throwaway home so
// config, state, the run log and generated extensions land in a temp dir,
// and a PATH of binDir plus the system dirs only. The system dirs keep git
// reachable for version stamping while hiding every agent binary the dev
// box happens to have installed, so the client picker shows exactly the
// stubs the test installed.
//
// TERM=dumb, not xterm: startup asks the terminal its color profile and a
// PTY never answers, which costs every run a five-second query timeout.
// Assertions strip escape sequences anyway, so color buys nothing here.
func hermeticEnv(t *testing.T, binDir string) []string {
	t.Helper()
	home := t.TempDir()
	// TS_AUTHKEY would authorize fresh bridge slots against the developer's
	// real tailnet; BROWSER is set to true below rather than dropped because
	// the login-link opener runs $BROWSER, and true is a no-op.
	drop := map[string]bool{
		"HOME": true, "XDG_CONFIG_HOME": true, "TERM": true, "PATH": true,
		"APERTURE_ENDPOINT": true, "APERTURE_BRIDGE": true,
		"TS_AUTHKEY": true, "BROWSER": true,
	}
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if !drop[k] {
			env = append(env, e)
		}
	}
	path := string(filepath.ListSeparator) + "/usr/bin" + string(filepath.ListSeparator) + "/bin"
	if binDir != "" {
		path = binDir + path
	}
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"TERM=dumb",
		"PATH="+path,
		"BROWSER=true",
	)
}

// stubPi is a stand-in for the pi binary: it records its argv and
// environment, and captures the generated provider extension while it still
// exists — aperture removes the extension when the child exits.
const stubPi = `#!/bin/sh
printf '%s\n' "$@" > "$APERTURE_E2E_RECORD/argv"
env > "$APERTURE_E2E_RECORD/env"
prev=""
for a in "$@"; do
	if [ "$prev" = "-e" ]; then
		cat "$a" > "$APERTURE_E2E_RECORD/extension"
	fi
	prev="$a"
done
`

// installStubPi writes the stub into binDir and returns the directory the
// stub records into.
func installStubPi(t *testing.T, binDir string) string {
	t.Helper()
	recordDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "pi"), []byte(stubPi), 0o755); err != nil {
		t.Fatal(err)
	}
	return recordDir
}

// waitForFile blocks until path exists and is non-empty.
func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return string(data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}
