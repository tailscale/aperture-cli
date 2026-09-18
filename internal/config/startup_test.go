package config_test

import (
	"path/filepath"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

// loadInto points config at a scratch directory and returns a Global holding
// the given settings, saved, so Resolve's writes have somewhere to go.
func loadInto(t *testing.T, s config.Settings) *config.Global {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	if err := config.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	g, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return g
}

func TestResolveFallsBackToTheSavedOne(t *testing.T) {
	g := loadInto(t, config.Settings{Endpoints: []config.Endpoint{{URL: "http://saved"}}})

	ep, err := config.Startup{}.Resolve(g)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep != (config.Endpoint{URL: "http://saved"}) {
		t.Errorf("endpoint = %+v, want the saved one", ep)
	}
}

func TestResolveTakesABareHost(t *testing.T) {
	g := loadInto(t, config.Settings{Endpoints: []config.Endpoint{{URL: "http://saved"}}})

	ep, err := config.Startup{URL: "aperture.example.com"}.Resolve(g)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep != (config.Endpoint{URL: "http://aperture.example.com"}) {
		t.Errorf("endpoint = %+v, want the named one, schemed", ep)
	}
}

// A named bridge with no URL is the scripted equivalent of picking a bridge in
// the connection picker, which starts at the well-known location rather than
// demanding a URL the user may not know.
func TestResolveGuessesTheLocationForANamedBridge(t *testing.T) {
	g := loadInto(t, config.Settings{Bridges: []config.Bridge{{ID: "bridge-abc123", Name: "Work"}}})

	ep, err := config.Startup{BridgeName: "work"}.Resolve(g)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep != (config.Endpoint{URL: config.DefaultLocation, BridgeID: "bridge-abc123"}) {
		t.Errorf("endpoint = %+v, want %s through the existing bridge", ep, config.DefaultLocation)
	}
	if len(g.Settings.Bridges) != 1 {
		t.Errorf("bridges = %+v, want the name matched rather than a second bridge made", g.Settings.Bridges)
	}
}

// The first scripted run has no bridge yet. Refusing there would mean the
// flag only works after someone has already done the thing by hand.
func TestResolveCreatesAnUnknownBridge(t *testing.T) {
	g := loadInto(t, config.Settings{})

	ep, err := config.Startup{URL: "http://aperture.example.com", BridgeName: "Work"}.Resolve(g)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(g.Settings.Bridges) != 1 || g.Settings.Bridges[0].Name != "Work" {
		t.Fatalf("bridges = %+v, want one called Work", g.Settings.Bridges)
	}
	if ep.BridgeID != g.Settings.Bridges[0].ID || ep.URL != "http://aperture.example.com" {
		t.Errorf("endpoint = %+v, want the URL through the new bridge", ep)
	}

	// Persisted, not just held: the next run has to match this bridge by name
	// instead of making another one.
	reloaded, err := config.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if len(reloaded.Bridges) != 1 {
		t.Errorf("saved bridges = %+v, want the new one written out", reloaded.Bridges)
	}
}

func TestResolveRejectsAUnusableURL(t *testing.T) {
	g := loadInto(t, config.Settings{})

	s := config.Startup{URL: "ftp://aperture.example.com"}
	if _, err := s.Resolve(g); err == nil {
		t.Error("Resolve accepted an ftp URL, want it refused before the TUI takes the terminal")
	}
}
