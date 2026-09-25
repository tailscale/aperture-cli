package bridges

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

// settingsGlobal is a Global whose settings are on disk under a throwaway
// config directory, so the writes the Attempt makes can be checked and broken.
func settingsGlobal(t *testing.T, s config.Settings) (*config.Global, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := config.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	return &config.Global{Settings: s}, filepath.Join(dir, "aperture")
}

// A failed RemoveEndpoint has to leave the attempt still ephemeral: otherwise
// the candidate it wrote stays in settings and nothing will ever take it out.
func TestAbandonStaysEphemeralWhenTheDropFails(t *testing.T) {
	g, settingsDir := settingsGlobal(t, config.Settings{Endpoints: []config.Endpoint{config.Direct("http://ai")}})
	a, err := BeginAttempt(g, config.Direct("http://new"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Ephemeral() {
		t.Fatal("an endpoint not in settings did not make the attempt ephemeral")
	}

	if err := os.Chmod(settingsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(settingsDir, 0o700) })
	if err := a.Abandon(g); err == nil {
		t.Fatal("Abandon wrote to a read-only settings directory")
	}
	if !a.Ephemeral() {
		t.Fatal("attempt forgot its candidate after a drop that did not happen")
	}

	if err := os.Chmod(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := a.Abandon(g); err != nil {
		t.Fatalf("Abandon after the directory came back: %v", err)
	}
	if len(g.Settings.Endpoints) != 1 {
		t.Errorf("endpoints = %+v, want the candidate gone", g.Settings.Endpoints)
	}
}

// switchingAttempt is an attempt through Work that has been asked to leave its
// tailnet, on a collection whose node answers Logout with logoutErr and then
// refuses to come up, so Run ends right after the switch.
func switchingAttempt(t *testing.T, logoutErr error) (*Attempt, *config.Global, *Machines) {
	t.Helper()
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work", Tailnet: "corp.example.com"}
	ep := config.Bridged("http://ai", bridge.ID)
	g, _ := settingsGlobal(t, config.Settings{Bridges: []config.Bridge{bridge}, Endpoints: []config.Endpoint{ep}})
	a, err := BeginAttempt(g, ep, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMachines(false)
	t.Cleanup(func() { m.Close() })
	m.newNode = func(config.Bridge, int, string, func(string, ...any), func(string, ...any)) tailnetNode {
		return &fakeNode{logoutErr: logoutErr, upErr: errors.New("no login yet")}
	}
	return a, g, m
}

// A logout the control plane refused has not happened. Retrying the attempt
// with the switch dropped would open the credentials still on disk and land
// back on the tailnet the user asked to leave.
func TestRetryKeepsTheSwitchUntilLogoutSucceeds(t *testing.T) {
	a, _, m := switchingAttempt(t, errors.New("control plane said no"))
	if _, err := a.Run(context.Background(), m, nil); err == nil {
		t.Fatal("Run succeeded with a logout the tailnet refused")
	}
	if !a.Retry().SwitchesTailnet() {
		t.Error("Retry dropped a switch whose logout never ran")
	}

	a, _, m = switchingAttempt(t, nil)
	if _, err := a.Run(context.Background(), m, nil); err == nil {
		t.Fatal("Run succeeded past a node that refuses to come up")
	}
	if a.Retry().SwitchesTailnet() {
		t.Error("Retry repeats a logout that already succeeded")
	}
}

// Typing a URL over a switch that has not logged out yet must not turn it
// into a plain reconnect to the old tailnet.
func TestRetargetKeepsAPendingSwitch(t *testing.T) {
	a, g, _ := switchingAttempt(t, nil)
	next, err := a.Retarget(g, config.Bridged("http://aperture.example.com", a.Bridge().ID))
	if err != nil {
		t.Fatal(err)
	}
	if !next.SwitchesTailnet() {
		t.Error("Retarget dropped the switch before its logout ran")
	}
}
