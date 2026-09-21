package config_test

import (
	"path/filepath"
	"strings"
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
	g := loadInto(t, config.Settings{Endpoints: []config.Endpoint{config.Direct("http://saved")}})

	ep, err := config.EndpointFromFlags(g, "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep != (config.Direct("http://saved")) {
		t.Errorf("endpoint = %+v, want the saved one", ep)
	}
}

func TestResolveTakesABareHost(t *testing.T) {
	g := loadInto(t, config.Settings{Endpoints: []config.Endpoint{config.Direct("http://saved")}})

	ep, err := config.EndpointFromFlags(g, "aperture.example.com", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep != (config.Direct("http://aperture.example.com")) {
		t.Errorf("endpoint = %+v, want the named one, schemed", ep)
	}
}

// A named bridge with no URL is the scripted equivalent of picking a bridge in
// the connection picker, which starts at the well-known location rather than
// demanding a URL the user may not know.
func TestResolveGuessesTheLocationForANamedBridge(t *testing.T) {
	g := loadInto(t, config.Settings{Bridges: []config.Bridge{{ID: "bridge-abc123", Name: "Work"}}})

	ep, err := config.EndpointFromFlags(g, "", "work")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep != (config.Bridged(config.DefaultLocation, "bridge-abc123")) {
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

	ep, err := config.EndpointFromFlags(g, "http://aperture.example.com", "Work")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(g.Settings.Bridges) != 1 || g.Settings.Bridges[0].Name != "Work" {
		t.Fatalf("bridges = %+v, want one called Work", g.Settings.Bridges)
	}
	if ep != config.Endpoint(config.Bridged("http://aperture.example.com", g.Settings.Bridges[0].ID)) {
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

	if _, err := config.EndpointFromFlags(g, "ftp://aperture.example.com", ""); err == nil {
		t.Error("Resolve accepted an ftp URL, want it refused before the TUI takes the terminal")
	}
}

// A refused invocation has to leave settings as it found them, or the run that
// exits with an error still costs the user a bridge to clean up by hand.
func TestResolveRejectsTheURLBeforeCreatingTheBridge(t *testing.T) {
	g := loadInto(t, config.Settings{})

	if _, err := config.EndpointFromFlags(g, "ftp://aperture.example.com", "Work"); err == nil {
		t.Fatal("Resolve accepted an ftp URL")
	}
	if len(g.Settings.Bridges) != 0 {
		t.Errorf("bridges = %+v, want none created for a rejected invocation", g.Settings.Bridges)
	}
	saved, err := config.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if len(saved.Bridges) != 0 {
		t.Errorf("saved bridges = %+v, want the rejected invocation to write nothing", saved.Bridges)
	}
}

// Names are the user's own labels and nothing keeps them unique, so a name that
// matches two bridges cannot address either of them.
func TestResolveRejectsAnAmbiguousBridgeName(t *testing.T) {
	g := loadInto(t, config.Settings{Bridges: []config.Bridge{
		{ID: "bridge-aaa111", Name: "Work"},
		{ID: "bridge-bbb222", Name: "work"},
	}})

	_, err := config.EndpointFromFlags(g, "", "WORK")
	if err == nil {
		t.Fatal("Resolve picked one of two bridges called work")
	}
	for _, id := range []string{"bridge-aaa111", "bridge-bbb222"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error %q does not name %s, so the user cannot tell them apart", err, id)
		}
	}
}
