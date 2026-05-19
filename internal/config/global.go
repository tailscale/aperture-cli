package config

import (
	"fmt"
	"strings"
)

// Global is the live mutable app-level state threaded through the TUI and
// every client package. It holds the current Aperture endpoint, the user's
// persisted settings, the last-launch record, and the provider list fetched
// from the active endpoint. Mutator methods persist to disk on success.
type Global struct {
	// ApertureHost is the currently active Aperture endpoint URL.
	ApertureHost string

	// Settings is the persisted user configuration (endpoint list, YOLO mode).
	Settings Settings

	// LastLaunch is the persisted record of the last successful client launch.
	LastLaunch LaunchState

	// Providers is the list returned from the active endpoint's /api/providers.
	// Populated by the TUI's preflight after a successful check.
	Providers []ProviderInfo

	// Debug enables verbose stderr dumps of env/args before each launch.
	// Not persisted; set from the --debug flag.
	Debug bool
}

// Load reads Settings and LaunchState from disk and returns a populated
// Global. The active ApertureHost is the first endpoint if any are configured,
// otherwise DefaultLocation. Providers is left empty for the TUI to populate
// after its preflight.
func Load() (*Global, error) {
	s, err := LoadSettings()
	if err != nil {
		return nil, err
	}
	ls, err := LoadState()
	if err != nil {
		return nil, err
	}
	host := DefaultLocation
	if len(s.Endpoints) > 0 {
		host = s.Endpoints[0].URL
	}
	return &Global{
		ApertureHost: host,
		Settings:     s,
		LastLaunch:   ls,
	}, nil
}

// SetYolo toggles YOLO mode and persists the new setting.
func (g *Global) SetYolo(on bool) error {
	g.Settings.YoloMode = on
	return SaveSettings(g.Settings)
}

// ActiveEndpoint returns the persisted endpoint currently selected by the
// user. The runtime ApertureHost may differ for bridge endpoints because it
// points at the local reverse proxy.
func (g *Global) ActiveEndpoint() Endpoint {
	if len(g.Settings.Endpoints) == 0 {
		return Endpoint{URL: DefaultLocation}
	}
	return g.Settings.Endpoints[0]
}

// SetActiveEndpoint rotates the endpoint to the front of the endpoint list
// (adding it if missing), updates ApertureHost to the endpoint URL, and
// persists. Bridge activation later rewrites ApertureHost to localhost.
func (g *Global) SetActiveEndpoint(ep Endpoint) error {
	g.ApertureHost = ep.URL
	eps := []Endpoint{ep}
	for _, ep := range g.Settings.Endpoints {
		if !sameEndpoint(ep, eps[0]) {
			eps = append(eps, ep)
		}
	}
	g.Settings.Endpoints = eps
	return SaveSettings(g.Settings)
}

// SetApertureHost rotates the direct URL to the front of the endpoint list
// (adding it if missing), updates ApertureHost, and persists.
func (g *Global) SetApertureHost(url string) error {
	return g.SetActiveEndpoint(Endpoint{URL: url})
}

// UpsertEndpoint appends the endpoint to the endpoint list if not already present,
// without changing which endpoint is active, and persists.
func (g *Global) UpsertEndpoint(ep Endpoint) error {
	for _, existing := range g.Settings.Endpoints {
		if sameEndpoint(existing, ep) {
			return nil
		}
	}
	g.Settings.Endpoints = append(g.Settings.Endpoints, ep)
	return SaveSettings(g.Settings)
}

// RemoveEndpoint deletes the endpoint at idx and persists. The active endpoint
// is kept pointing at index 0 after removal; callers are responsible for
// re-running preflight if the active endpoint changed.
func (g *Global) RemoveEndpoint(idx int) error {
	if idx < 0 || idx >= len(g.Settings.Endpoints) {
		return nil
	}
	eps := make([]Endpoint, 0, len(g.Settings.Endpoints)-1)
	eps = append(eps, g.Settings.Endpoints[:idx]...)
	eps = append(eps, g.Settings.Endpoints[idx+1:]...)
	g.Settings.Endpoints = eps
	if len(eps) > 0 {
		g.ApertureHost = eps[0].URL
	}
	return SaveSettings(g.Settings)
}

// AddBridge creates, saves, and returns a bridge with a generated stable ID.
func (g *Global) AddBridge(name string) (Bridge, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Bridge{}, fmt.Errorf("bridge name is empty")
	}
	id, err := newBridgeID(g.Settings.Bridges)
	if err != nil {
		return Bridge{}, err
	}
	p := Bridge{ID: id, Name: name}
	g.Settings.Bridges = append(g.Settings.Bridges, p)
	if err := SaveSettings(g.Settings); err != nil {
		return Bridge{}, err
	}
	return p, nil
}

// RemoveBridge deletes a bridge if no endpoint still references it.
func (g *Global) RemoveBridge(id string) error {
	for _, ep := range g.Settings.Endpoints {
		if ep.BridgeID == id {
			return fmt.Errorf("bridge is used by endpoint %s", ep.URL)
		}
	}
	for i, p := range g.Settings.Bridges {
		if p.ID != id {
			continue
		}
		g.Settings.Bridges = append(g.Settings.Bridges[:i], g.Settings.Bridges[i+1:]...)
		return SaveSettings(g.Settings)
	}
	return nil
}

// Bridge returns the configured bridge with id.
func (g *Global) Bridge(id string) (Bridge, bool) {
	for _, p := range g.Settings.Bridges {
		if p.ID == id {
			return p, true
		}
	}
	return Bridge{}, false
}

// RecordLaunch stores the launch record to disk and updates the in-memory copy.
func (g *Global) RecordLaunch(s LaunchState) error {
	g.LastLaunch = s
	return SaveState(s)
}

// Provider returns the ProviderInfo for id, or a zero value and false if no
// such provider is in g.Providers.
func (g *Global) Provider(id string) (ProviderInfo, bool) {
	for _, p := range g.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderInfo{}, false
}
