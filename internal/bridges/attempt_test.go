package bridges

import (
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

// A failed DropEndpoint has to leave the attempt still ephemeral: otherwise
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
