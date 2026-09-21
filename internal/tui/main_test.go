package tui

import (
	"os"
	"testing"
)

// TestMain points every test at a throwaway config directory. A test that
// writes settings without isolating itself overwrote the developer's real
// settings.json on 2026-09-21; per-test t.Setenv still applies on top.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "aperture-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", tmp)
	os.Setenv("XDG_CONFIG_HOME", tmp+"/.config")
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}
