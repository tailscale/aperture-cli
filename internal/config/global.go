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

	// Providers is the provider-level view aggregated from the active
	// endpoint's /v1/models response.
	// Populated by the TUI's preflight after a successful check.
	Providers []ProviderInfo

	// Debug enables bridge diagnostics and verbose stderr dumps of env/args
	// before each launch. Not persisted; set from the --debug flag.
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
	eps := []Endpoint{ep}
	for _, existing := range g.Settings.Endpoints {
		if !sameEndpoint(existing, ep) {
			eps = append(eps, existing)
		}
	}
	next := g.Settings
	next.Endpoints = eps
	if err := SaveSettings(next); err != nil {
		return err
	}
	g.Settings = next
	g.ApertureHost = ep.URL
	return nil
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
	next := g.Settings
	next.Endpoints = append(append([]Endpoint(nil), g.Settings.Endpoints...), ep)
	if err := SaveSettings(next); err != nil {
		return err
	}
	g.Settings = next
	return nil
}

// ReplaceEndpoint replaces old with next in place and persists the result.
// It does not change which endpoint is active unless old is already active.
func (g *Global) ReplaceEndpoint(old, next Endpoint) error {
	eps := append([]Endpoint(nil), g.Settings.Endpoints...)
	oldIdx := -1
	for i, existing := range eps {
		if !sameEndpoint(existing, old) {
			continue
		}
		oldIdx = i
		break
	}
	if oldIdx < 0 {
		return fmt.Errorf("endpoint %s is not configured", old.URL)
	}
	eps[oldIdx] = next
	deduped := eps[:0]
	for _, ep := range eps {
		duplicate := false
		for _, existing := range deduped {
			if sameEndpoint(existing, ep) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			deduped = append(deduped, ep)
		}
	}
	updated := g.Settings
	updated.Endpoints = deduped
	if err := SaveSettings(updated); err != nil {
		return err
	}
	g.Settings = updated
	if oldIdx == 0 {
		g.ApertureHost = next.URL
	}
	return nil
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
	next := g.Settings
	next.Endpoints = eps
	if err := SaveSettings(next); err != nil {
		return err
	}
	g.Settings = next
	if idx == 0 && len(eps) > 0 {
		g.ApertureHost = eps[0].URL
	}
	return nil
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
	next := g.Settings
	next.Bridges = append(append([]Bridge(nil), g.Settings.Bridges...), p)
	if err := SaveSettings(next); err != nil {
		return Bridge{}, err
	}
	g.Settings = next
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
		next := g.Settings
		next.Bridges = append([]Bridge(nil), g.Settings.Bridges[:i]...)
		next.Bridges = append(next.Bridges, g.Settings.Bridges[i+1:]...)
		if err := SaveSettings(next); err != nil {
			return err
		}
		g.Settings = next
		return nil
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
	ep := g.ActiveEndpoint()
	s.LastEndpointURL = ep.URL
	s.LastBridgeID = ep.BridgeID
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
