//go:build unix

package e2e

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The bridge concurrency contract (ADR 0006, README "Concurrent sessions")
// tested through the built binary: every expectation here is a file the
// program writes, a lock it holds, or a screen it paints. The data plane is
// out of reach — without tailnet credentials no test finishes a login — so
// "both sessions reach Aperture" is not asserted, only the local half of the
// contract: distinct slots, held locks, blocked removal, the 100-slot cap.

// isRunning reports whether the spawned process is alive.
func (term *terminal) isRunning() bool {
	return term.cmd.Process.Signal(syscall.Signal(0)) == nil
}

// waitForState polls cond until it holds, failing with what after timeout.
// For assertions on the filesystem, where there is no screen text to wait
// on. Removal gets a long budget: logout is a control-plane round trip.
func waitForState(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// apertureConfigDir recovers the aperture configuration directory from a
// hermetic environment, so the test can assert on the files the run writes.
func apertureConfigDir(t *testing.T, env []string) string {
	t.Helper()
	for _, e := range env {
		if dir, ok := strings.CutPrefix(e, "XDG_CONFIG_HOME="); ok {
			return filepath.Join(dir, "aperture")
		}
	}
	t.Fatal("environment has no XDG_CONFIG_HOME")
	return ""
}

// lockPath is the lock file for one slot of the bridge, e.g.
// bridges/locks/70da61-2.lock.
func lockPath(configDir, suffix string, slot int) string {
	return filepath.Join(configDir, "bridges", "locks", fmt.Sprintf("%s-%d.lock", suffix, slot))
}

// slotSuffix reads the bridge's slot prefix back off disk: the name of its
// slot-1 lock file with "-1.lock" trimmed, e.g. locks/70da61-1.lock → 70da61.
func slotSuffix(configDir string) (string, bool) {
	matches, _ := filepath.Glob(lockPath(configDir, "*", 1))
	if len(matches) == 0 {
		return "", false
	}
	return strings.TrimSuffix(filepath.Base(matches[0]), "-1.lock"), true
}

// waitForFirstSlot waits for a process to claim slot 1 and returns the
// bridge's slot suffix.
func waitForFirstSlot(t *testing.T, configDir string) string {
	t.Helper()
	var suffix string
	waitForState(t, "slot 1 to be claimed", 15*time.Second, func() bool {
		s, ok := slotSuffix(configDir)
		if !ok {
			return false
		}
		suffix = s
		return isLocked(t, lockPath(configDir, suffix, 1))
	})
	return suffix
}

// isLocked reports whether the slot lock file at path is held by a live
// process, by attempting the same non-blocking exclusive flock the slot
// claim takes (ADR 0006 decision 2). A missing file is not held.
func isLocked(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	switch err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); {
	case err == nil:
		if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
			t.Fatalf("unlock %s: %v", path, err)
		}
		return false
	case errors.Is(err, unix.EWOULDBLOCK):
		return true
	default:
		t.Fatalf("probe %s: %v", path, err)
		return false
	}
}

// removeBridgeViaMenu drives a freshly spawned launcher from the
// getting-started screen to the bridge removal confirmation: connection
// options, the endpoint row reached through bridge "work", remove, confirm.
func removeBridgeViaMenu(t *testing.T, term *terminal) {
	t.Helper()
	term.waitFor(t, "Retry connection")
	term.send("3")
	term.waitFor(t, "Aperture Endpoints")
	row := regexp.MustCompile(`\[(\d+)\][^\n]*via work`).FindStringSubmatch(term.screen())
	if row == nil {
		t.Fatalf("no endpoint via work in:\n%s", term.screen())
	}
	term.send(row[1])
	term.waitFor(t, "Remove connection")
	term.send("4")
	term.waitFor(t, "Remove bridge work?")
	term.send("y")
}

// ADR 0006 decisions 1 and 2: each concurrent process on a bridge claims its
// own numbered slot — its own state directory and held lock — instead of
// every process registering the same node key and letting the control plane
// hand the session to whichever registered last.
func TestConcurrentProcessesGetOwnSlots(t *testing.T) {
	env := hermeticEnv(t, "")
	dir := apertureConfigDir(t, env)

	first := spawn(t, apertureBin, []string{"-bridge", "work"}, env)
	suffix := waitForFirstSlot(t, dir)

	second := spawn(t, apertureBin, []string{"-bridge", "work"}, env)
	waitForState(t, "second process to claim slot 2", 15*time.Second, func() bool {
		return isLocked(t, lockPath(dir, suffix, 2))
	})

	if !first.isRunning() || !second.isRunning() {
		t.Fatalf("both processes stay running: first=%v second=%v", first.isRunning(), second.isRunning())
	}
	if !isLocked(t, lockPath(dir, suffix, 1)) {
		t.Error("second process released the first's slot")
	}
	for _, d := range []string{suffix, suffix + "-2"} {
		if _, err := os.Stat(filepath.Join(dir, "bridges", d)); err != nil {
			t.Errorf("state directory %s: %v", d, err)
		}
	}
}

// ADR 0006 decision 2: a slot is held for the node's life and released when
// the process dies, so the next process claims the lowest free slot rather
// than minting a new device per launch.
func TestSlotReusedAfterProcessDeath(t *testing.T) {
	env := hermeticEnv(t, "")
	dir := apertureConfigDir(t, env)

	spawn(t, apertureBin, []string{"-bridge", "work"}, env)
	suffix := waitForFirstSlot(t, dir)

	second := spawn(t, apertureBin, []string{"-bridge", "work"}, env)
	waitForState(t, "second process to claim slot 2", 15*time.Second, func() bool {
		return isLocked(t, lockPath(dir, suffix, 2))
	})

	second.kill()
	waitForState(t, "slot 2 to be released", 10*time.Second, func() bool {
		return !isLocked(t, lockPath(dir, suffix, 2))
	})

	spawn(t, apertureBin, []string{"-bridge", "work"}, env)
	waitForState(t, "third process to reclaim slot 2", 15*time.Second, func() bool {
		return isLocked(t, lockPath(dir, suffix, 2))
	})
	if _, err := os.Stat(lockPath(dir, suffix, 3)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("slot 3 claimed while slot 2 was free (stat: %v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bridges", suffix+"-3")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("state directory for slot 3 created while slot 2 was free (stat: %v)", err)
	}
}

// ADR 0006 decision 3: removing a bridge whose slot is held by a live
// process fails before anything is logged out, naming the conflict; once
// the process is gone, the same removal destroys every slot.
func TestRemovalBlockedByLiveProcess(t *testing.T) {
	env := hermeticEnv(t, "")
	dir := apertureConfigDir(t, env)

	holder := spawn(t, apertureBin, []string{"-bridge", "work"}, env)
	suffix := waitForFirstSlot(t, dir)

	// The menu instance gets an unreachable endpoint so its startup attempt
	// fails fast everywhere, tailnet or not, and lands on the menu.
	menuEnv := append([]string{"APERTURE_ENDPOINT=http://127.0.0.1:9"}, env...)
	menu := spawn(t, apertureBin, nil, menuEnv)
	removeBridgeViaMenu(t, menu)
	menu.waitFor(t, "in use by another aperture process")

	settings := filepath.Join(dir, "settings.json")
	if b, err := os.ReadFile(settings); err != nil || !strings.Contains(string(b), suffix) {
		t.Errorf("blocked removal changed settings: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bridges", suffix)); err != nil {
		t.Error("blocked removal discarded the state directory:", err)
	}
	if !isLocked(t, lockPath(dir, suffix, 1)) {
		t.Error("blocked removal released the live process's slot")
	}

	holder.kill()
	waitForState(t, "slot 1 to be released", 10*time.Second, func() bool {
		return !isLocked(t, lockPath(dir, suffix, 1))
	})

	// Driven the same way with no live process, removal now destroys the
	// bridge.
	retry := spawn(t, apertureBin, nil, menuEnv)
	removeBridgeViaMenu(t, retry)
	waitForState(t, "bridge removal to finish", 90*time.Second, func() bool {
		b, err := os.ReadFile(settings)
		if err != nil || strings.Contains(string(b), suffix) {
			return false
		}
		_, err = os.Stat(filepath.Join(dir, "bridges", suffix))
		return errors.Is(err, fs.ErrNotExist)
	})
	if b, err := os.ReadFile(settings); err != nil || !strings.Contains(string(b), "http://ai") {
		t.Errorf("removal took the direct endpoints with it: %v", err)
	}
}

// ADR 0006 decision 4: past 100 claimed slots the process errors instead of
// probing forever. The test holds the locks itself — an exclusive flock on
// the slot file is exactly what a live process presents (decision 2).
func TestSlotCap(t *testing.T) {
	env := hermeticEnv(t, "")
	dir := apertureConfigDir(t, env)

	first := spawn(t, apertureBin, []string{"-bridge", "work"}, env)
	suffix := waitForFirstSlot(t, dir)
	first.kill()
	waitForState(t, "slot 1 to be released", 10*time.Second, func() bool {
		return !isLocked(t, lockPath(dir, suffix, 1))
	})

	var held []*os.File
	t.Cleanup(func() {
		for _, f := range held {
			f.Close()
		}
	})
	for i := range 100 {
		f, err := os.OpenFile(lockPath(dir, suffix, i+1), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatalf("create slot %d lock: %v", i+1, err)
		}
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatalf("hold slot %d: %v", i+1, err)
		}
		held = append(held, f)
	}

	capped := spawn(t, apertureBin, []string{"-bridge", "work"}, env)
	capped.waitFor(t, "more than 100 aperture processes on bridge")
	if _, err := os.Stat(lockPath(dir, suffix, 101)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("slot 101 claimed past the cap (stat: %v)", err)
	}
}
