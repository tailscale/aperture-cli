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
	g := loadInto(t, config.Settings{Endpoints: []config.EndpointEntry{{Endpoint: config.Direct("http://saved")}}})

	ep, err := config.EndpointFromFlags(g, "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep != (config.Direct("http://saved")) {
		t.Errorf("endpoint = %+v, want the saved one", ep)
	}
}

func TestResolveTakesABareHost(t *testing.T) {
	g := loadInto(t, config.Settings{Endpoints: []config.EndpointEntry{{Endpoint: config.Direct("http://saved")}}})

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

func TestAddBridgeStoresThePort(t *testing.T) {
	g := loadInto(t, config.Settings{})

	b, err := g.AddBridge("Work", 8991)
	if err != nil {
		t.Fatalf("AddBridge: %v", err)
	}
	if b.Port != 8991 {
		t.Errorf("port = %d, want 8991", b.Port)
	}
	saved, err := config.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if len(saved.Bridges) != 1 || saved.Bridges[0].Port != 8991 {
		t.Errorf("saved bridges = %+v, want the port written out", saved.Bridges)
	}
}

// Bridge names are the command-line handle, so two bridges may not share one.
// Duplicates from hand-edited files still error at resolution instead.
func TestAddBridgeRefusesADuplicateName(t *testing.T) {
	g := loadInto(t, config.Settings{Bridges: []config.Bridge{{ID: "bridge-abc123", Name: "Work"}}})

	if _, err := g.AddBridge("work", 0); err == nil {
		t.Error("AddBridge took a second bridge called work")
	}
	if len(g.Settings.Bridges) != 1 {
		t.Errorf("bridges = %+v, want the duplicate refused in memory too", g.Settings.Bridges)
	}
}

func TestAddBridgeRefusesABadPort(t *testing.T) {
	g := loadInto(t, config.Settings{})

	for _, port := range []int{-1, 65536} {
		if _, err := g.AddBridge("Work", port); err == nil {
			t.Errorf("AddBridge took port %d", port)
		}
	}
}

func TestNameEndpoint(t *testing.T) {
	g := loadInto(t, config.Settings{Endpoints: []config.EndpointEntry{{Endpoint: config.Direct("http://ai")}}})

	if err := g.NameEndpoint(config.Direct("http://ai"), "e1"); err != nil {
		t.Fatalf("NameEndpoint: %v", err)
	}
	if len(g.Settings.Endpoints) != 1 || g.Settings.Endpoints[0].Name != "e1" {
		t.Fatalf("endpoints = %+v, want e1 named in place", g.Settings.Endpoints)
	}

	// A second destination gets its own row; a taken name and a URL-looking
	// name are refused.
	if err := g.NameEndpoint(config.Direct("http://other"), "E1"); err == nil {
		t.Error("NameEndpoint took a name another endpoint holds")
	}
	if err := g.NameEndpoint(config.Direct("http://other"), "http://ai"); err == nil {
		t.Error("NameEndpoint took a name that parses as a URL")
	}
	if err := g.NameEndpoint(config.Direct("http://other"), "e2"); err != nil {
		t.Fatalf("NameEndpoint: %v", err)
	}
	if len(g.Settings.Endpoints) != 2 || g.Settings.Endpoints[1].Name != "e2" {
		t.Errorf("endpoints = %+v, want e2 appended", g.Settings.Endpoints)
	}

	saved, err := config.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if len(saved.Endpoints) != 2 || saved.Endpoints[0].Name != "e1" || saved.Endpoints[1].Name != "e2" {
		t.Errorf("saved endpoints = %+v, want both names written out", saved.Endpoints)
	}
}

func TestResolveTakesAnEndpointName(t *testing.T) {
	g := loadInto(t, config.Settings{
		Bridges: []config.Bridge{{ID: "bridge-abc123", Name: "Work"}},
		Endpoints: []config.EndpointEntry{
			{Endpoint: config.Direct("http://ai"), Name: "e1"},
			{Endpoint: config.Bridged("http://aperture.example.com", "bridge-abc123"), Name: "e2"},
		},
	})

	// Names match without case, and a named bridged endpoint keeps its bridge.
	ep, err := config.EndpointFromFlags(g, "E1", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep != (config.Direct("http://ai")) {
		t.Errorf("endpoint = %+v, want the one named e1", ep)
	}
	ep, err = config.EndpointFromFlags(g, "e2", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep != (config.Bridged("http://aperture.example.com", "bridge-abc123")) {
		t.Errorf("endpoint = %+v, want the one named e2 with its bridge", ep)
	}

	// The bridge flag may confirm the bridge a named endpoint uses, but not
	// contradict it, and it means nothing against a direct endpoint.
	if _, err := config.EndpointFromFlags(g, "e2", "work"); err != nil {
		t.Errorf("Resolve refused the bridge e2 already uses: %v", err)
	}
	if _, err := config.EndpointFromFlags(g, "e2", "other"); err == nil {
		t.Error("Resolve moved e2 to a bridge it does not use")
	}
	if _, err := config.EndpointFromFlags(g, "e1", "work"); err == nil {
		t.Error("Resolve pinned a bridge onto a direct endpoint")
	}
	if len(g.Settings.Bridges) != 1 {
		t.Errorf("bridges = %+v, want rejected combinations to create nothing", g.Settings.Bridges)
	}
}

// A name that matches nothing is a host, which is what bare hosts have always
// meant on this flag.
func TestResolveFallsBackToAURLForAnUnknownName(t *testing.T) {
	g := loadInto(t, config.Settings{})

	ep, err := config.EndpointFromFlags(g, "e1", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ep != (config.Direct("http://e1")) {
		t.Errorf("endpoint = %+v, want e1 read as a bare host", ep)
	}
}

// Names are tab-separated output a script parses and a status line a user
// reads, so a newline would forge a row.
func TestRefusesControlCharactersInNames(t *testing.T) {
	g := loadInto(t, config.Settings{})
	if _, err := g.AddBridge("bad\tbridge", 0); err == nil {
		t.Error("AddBridge took a tab in a name")
	}
	if err := g.NameEndpoint(config.Direct("http://ai"), "bad\nendpoint"); err == nil {
		t.Error("NameEndpoint took a newline in a name")
	}
}
