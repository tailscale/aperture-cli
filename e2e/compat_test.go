//go:build tailnet

package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// piRowPattern finds the Pi row number in a client picker's screen.
var piRowPattern = regexp.MustCompile(`\[(\d)\]\s+Pi`)

// Cross-version compatibility, black-box: the latest RELEASED binary writes
// its state, and this branch's binary reads it. The backward-compat claim
// ("old files load unchanged") is tested against what users actually run,
// not a hand-built fixture. Run with `go test -tags tailnet ./e2e/`.
func TestCompatOldStateReadsWithNewBinary(t *testing.T) {
	srv := fakeAperture(t)
	old := releaseBinary(t)

	// The released binary writes old-format settings and state: a direct
	// endpoint from a full connect, and a bridge created by the -bridge flag
	// (which saves at flag resolution, login or not).
	env := hermeticEnv(t, "")
	term := spawn(t, old, []string{"-endpoint", srv.URL}, env)
	term.waitFor(t, "Which editor do you want to use?")
	term.send("q")
	term.waitExit(t)

	bridgeTerm := spawn(t, old, []string{"-bridge", "work"}, env)
	// The bridge attempt hangs at login with no credentials; give the flag
	// resolution time to save the bridge, then kill the attempt.
	time.Sleep(2 * time.Second)
	bridgeTerm.kill()

	// This branch's binary reads every bit of it.
	stdout, stderr, err := run(t, apertureBin, env, "bridge", "list")
	if err != nil {
		t.Fatalf("bridge list on old state: %v (%s)", err, stderr)
	}
	if !strings.Contains(stdout, "work") {
		t.Errorf("bridge list = %q, want the bridge the release binary wrote", stdout)
	}
	stdout, stderr, err = run(t, apertureBin, env, "endpoint", "list")
	if err != nil {
		t.Fatalf("endpoint list on old state: %v (%s)", err, stderr)
	}
	if !strings.Contains(stdout, srv.URL) {
		t.Errorf("endpoint list = %q, want the endpoint the release binary verified", stdout)
	}

	// And it can launch against old state without rewriting it.
	binDir := t.TempDir()
	recordDir := installStubPi(t, binDir)
	launchEnv := append(hermeticEnv(t, binDir), "APERTURE_E2E_RECORD="+recordDir)
	// Reuse the same home: hermeticEnv made a fresh one, so point the launch
	// at the old-state home's config dir explicitly.
	settingsHome := xdgFromEnv(t, env)
	launchEnv = append(launchEnv, "XDG_CONFIG_HOME="+settingsHome)
	if _, stderr, err := run(t, apertureBin, launchEnv, "pi", "--endpoint", srv.URL); err != nil {
		t.Fatalf("launch on old state: %v (%s)", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(recordDir, "argv")); err != nil {
		t.Error("the client did not launch on old state")
	}
}

// xdgFromEnv recovers XDG_CONFIG_HOME from a hermetic environment.
func xdgFromEnv(t *testing.T, env []string) string {
	t.Helper()
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "XDG_CONFIG_HOME="); ok {
			return v
		}
	}
	t.Fatal("environment has no XDG_CONFIG_HOME")
	return ""
}

// With TS_AUTHKEY, the released binary logs a bridge in on the real tailnet
// and this branch's binary launches through it reusing the slot-1
// credentials: no login link may appear. Without the key there is no
// tailnet to prove this on, so the test skips.
func TestCompatOldBridgeLoginReusedByNewBinary(t *testing.T) {
	key := os.Getenv("TS_AUTHKEY")
	if key == "" {
		t.Skip("TS_AUTHKEY unset: no tailnet to log a bridge into")
	}
	old := releaseBinary(t)
	binDir := t.TempDir()
	recordDir := installStubPi(t, binDir)
	env := append(hermeticEnv(t, binDir), "APERTURE_E2E_RECORD="+recordDir, "TS_AUTHKEY="+key)

	t.Cleanup(func() {
		// Log the bridge out of the tailnet so it leaves no machine behind.
		_, _, _ = run(t, apertureBin, env, "bridge", "delete", "compatbridge", "--force")
	})

	// The released binary logs the bridge in and launches the stub through
	// it against the tailnet's own Aperture.
	term := spawn(t, old, []string{"-bridge", "compatbridge", "-endpoint", "http://ai"}, env)
	term.waitFor(t, "Which editor do you want to use?")
	// Row numbers shift with whatever clients the box has installed, so read
	// the picker instead of guessing.
	row := piRowPattern.FindStringSubmatch(term.screen())
	if row == nil {
		t.Fatalf("no Pi row in the release binary's picker:\n%s", term.screen())
	}
	term.send(row[1])
	// The real Aperture answers /v1/models with many providers, so the
	// release walks its provider, backend and model menus before launching.
	// Descend them with the first row until the stub records its argv.
	argv := filepath.Join(recordDir, "argv")
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(argv); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no launch after descending the release's menus:\n%s", term.screen())
		}
		term.send("1")
		time.Sleep(time.Second)
	}
	term.send("q")
	term.waitExit(t)

	// This branch launches through the same bridge. The slot-1 state the
	// release wrote must authorize without a login link.
	if err := os.Remove(filepath.Join(recordDir, "argv")); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := run(t, apertureBin, env, "pi", "--bridge", "compatbridge", "--endpoint", "http://ai")
	if err != nil {
		t.Fatalf("launch through the old bridge: %v (%s)", err, stderr)
	}
	if strings.Contains(stderr, "log in") {
		t.Errorf("stderr = %q, want no login link: the old login should be reused", stderr)
	}
	if _, err := os.Stat(filepath.Join(recordDir, "argv")); err != nil {
		t.Error("the client did not launch through the old bridge")
	}
}
