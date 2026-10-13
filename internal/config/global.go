package config

import (
	"fmt"
	"strings"
	"unicode"
)

// Global holds the live app state the TUI and every client package share:
// the current Aperture URL, the user's saved settings, the last-launch record
// and the providers fetched from the active endpoint. Every mutator method
// writes to disk before it changes the in-memory copy.
type Global struct {
	// ApertureHost is the URL clients send requests to right now.
	ApertureHost string

	// Settings holds the saved user configuration.
	Settings Settings

	// LastLaunch records the last successful client launch.
	LastLaunch LaunchState

	// Providers lists the providers the active endpoint answered /v1/models
	// with. The TUI's preflight fills it after a successful check.
	Providers []ProviderInfo

	// Debug turns on bridge diagnostics and dumps env and args to stderr
	// before each launch. Not saved; set from the --debug flag.
	Debug bool
}

// Load reads Settings and LaunchState from disk and returns a Global.
// ApertureHost starts as the first configured endpoint, or DefaultLocation
// when there is none. Providers stays empty until the TUI's preflight fills
// it.
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
		host = s.Endpoints[0].Endpoint.URL()
	}
	return &Global{
		ApertureHost: host,
		Settings:     s,
		LastLaunch:   ls,
	}, nil
}

// SetYolo sets YOLO mode and saves it.
func (g *Global) SetYolo(on bool) error {
	g.Settings.YoloMode = on
	return SaveSettings(g.Settings)
}

// ActiveEndpoint returns the saved endpoint the user selected. ApertureHost
// can differ from its URL for a bridged endpoint, because ApertureHost then
// points at the local reverse proxy.
func (g *Global) ActiveEndpoint() Endpoint {
	if len(g.Settings.Endpoints) == 0 {
		return Direct(DefaultLocation)
	}
	return g.Settings.Endpoints[0].Endpoint
}

// SetActiveEndpoint moves ep to the front of the endpoint list, adding it if
// missing, sets ApertureHost to its URL and saves. replacing is the original
// of a verified URL edit and goes in the same write; pass nil otherwise.
// Bridge activation later rewrites ApertureHost to the local proxy.
func (g *Global) SetActiveEndpoint(ep Endpoint, replacing Endpoint) error {
	head := EndpointEntry{Endpoint: ep}
	for _, existing := range g.Settings.Endpoints {
		if existing.Endpoint == ep {
			head = existing
		}
	}
	eps := endpointList{head}
	for _, existing := range g.Settings.Endpoints {
		if existing.Endpoint != ep && existing.Endpoint != replacing {
			eps = append(eps, existing)
		}
	}
	next := g.Settings
	next.Endpoints = eps
	if err := SaveSettings(next); err != nil {
		return err
	}
	g.Settings = next
	g.ApertureHost = ep.URL()
	return nil
}

// SetApertureHost makes the direct endpoint at url active. See
// SetActiveEndpoint.
func (g *Global) SetApertureHost(url string) error {
	return g.SetActiveEndpoint(Direct(url), nil)
}

// UpsertEndpoint appends ep to the endpoint list when it is not already
// there and saves. The active endpoint does not change.
func (g *Global) UpsertEndpoint(ep Endpoint) error {
	for _, existing := range g.Settings.Endpoints {
		if existing.Endpoint == ep {
			return nil
		}
	}
	next := g.Settings
	next.Endpoints = append(append(endpointList(nil), g.Settings.Endpoints...), EndpointEntry{Endpoint: ep})
	if err := SaveSettings(next); err != nil {
		return err
	}
	g.Settings = next
	return nil
}

// ReplaceEndpoint puts next where old was and saves. The active endpoint
// changes only when old was the active one.
func (g *Global) ReplaceEndpoint(old, next Endpoint) error {
	eps := append(endpointList(nil), g.Settings.Endpoints...)
	oldIdx := -1
	for i, existing := range eps {
		if existing.Endpoint != old {
			continue
		}
		oldIdx = i
		break
	}
	if oldIdx < 0 {
		return fmt.Errorf("endpoint %s is not configured", old.URL())
	}
	// A URL edit keeps the row's name: the handle is the user's, not the URL's.
	eps[oldIdx] = EndpointEntry{Endpoint: next, Name: eps[oldIdx].Name}
	deduped := eps[:0]
	for _, ep := range eps {
		duplicate := false
		for _, existing := range deduped {
			if existing.Endpoint == ep.Endpoint {
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
		g.ApertureHost = next.URL()
	}
	return nil
}

// removeEndpointAt deletes the endpoint at idx and saves. Index 0 stays the
// active endpoint, so removing index 0 promotes the next one. Callers rerun
// preflight when the active endpoint changed.
func (g *Global) removeEndpointAt(idx int) error {
	if idx < 0 || idx >= len(g.Settings.Endpoints) {
		return nil
	}
	eps := make(endpointList, 0, len(g.Settings.Endpoints)-1)
	eps = append(eps, g.Settings.Endpoints[:idx]...)
	eps = append(eps, g.Settings.Endpoints[idx+1:]...)
	next := g.Settings
	next.Endpoints = eps
	if err := SaveSettings(next); err != nil {
		return err
	}
	g.Settings = next
	if idx == 0 && len(eps) > 0 {
		g.ApertureHost = eps[0].Endpoint.URL()
	}
	return nil
}

// RemoveEndpoint removes ep from the list and saves. The active endpoint is
// never dropped: it is the connection the user falls back to. An endpoint
// not in the list is not an error.
func (g *Global) RemoveEndpoint(ep Endpoint) error {
	for i, existing := range g.Settings.Endpoints {
		if i == 0 || existing.Endpoint != ep {
			continue
		}
		return g.removeEndpointAt(i)
	}
	return nil
}

// checkName refuses a name with control characters. Names reach the tabbed
// stdout of bridge list and endpoint list and the status lines scripts
// parse, and a newline or escape would forge rows in them.
func checkName(name string) error {
	if strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("name %q contains control characters", name)
	}
	return nil
}

// AddBridge creates a bridge named name with a generated ID, saves it and
// returns it. port is the fixed port serve binds, zero for none. The name
// is the bridge's command-line handle, so a duplicate is refused without
// case.
func (g *Global) AddBridge(name string, port int) (Bridge, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Bridge{}, fmt.Errorf("bridge name is empty")
	}
	if err := checkName(name); err != nil {
		return Bridge{}, err
	}
	if port < 0 || port > 65535 {
		return Bridge{}, fmt.Errorf("port %d is outside 0-65535", port)
	}
	for _, b := range g.Settings.Bridges {
		if strings.EqualFold(b.Name, name) {
			return Bridge{}, fmt.Errorf("a bridge called %q already exists", name)
		}
	}
	id, err := newBridgeID(g.Settings.Bridges)
	if err != nil {
		return Bridge{}, err
	}
	p := Bridge{ID: id, Name: name, Port: port}
	next := g.Settings
	next.Bridges = append(append([]Bridge(nil), g.Settings.Bridges...), p)
	if err := SaveSettings(next); err != nil {
		return Bridge{}, err
	}
	g.Settings = next
	return p, nil
}

// SetBridgeTailnet records the tailnet bridge id logged in to and saves. An
// unknown bridge is not an error. The user may have deleted it while the
// connection that reported the name was still coming up.
func (g *Global) SetBridgeTailnet(id, tailnet string) error {
	for i, p := range g.Settings.Bridges {
		if p.ID != id || p.Tailnet == tailnet {
			continue
		}
		next := g.Settings
		next.Bridges = append([]Bridge(nil), g.Settings.Bridges...)
		next.Bridges[i].Tailnet = tailnet
		if err := SaveSettings(next); err != nil {
			return err
		}
		g.Settings = next
		return nil
	}
	return nil
}

// RemoveBridge deletes bridge id and saves. It refuses while an endpoint
// still connects through the bridge.
func (g *Global) RemoveBridge(id string) error {
	for _, entry := range g.Settings.Endpoints {
		if ep, ok := entry.Endpoint.(BridgeEndpoint); ok && ep.BridgeID() == id {
			return fmt.Errorf("bridge is used by endpoint %s", ep.URL())
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

// NameEndpoint saves name as the handle of ep's row in the endpoint list,
// appending ep when it is not listed yet. A name another endpoint holds is
// refused, and so is a name containing ://, because the command line reads
// those as URLs.
func (g *Global) NameEndpoint(ep Endpoint, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("endpoint name is empty")
	}
	if strings.Contains(name, "://") {
		return fmt.Errorf("endpoint name %q looks like a URL", name)
	}
	if err := checkName(name); err != nil {
		return err
	}
	next := g.Settings
	next.Endpoints = append(endpointList(nil), g.Settings.Endpoints...)
	found := false
	for i, entry := range next.Endpoints {
		if entry.Endpoint != ep && entry.Name != "" && strings.EqualFold(entry.Name, name) {
			return fmt.Errorf("an endpoint called %q already exists", name)
		}
		if entry.Endpoint == ep {
			next.Endpoints[i].Name = name
			found = true
		}
	}
	if !found {
		next.Endpoints = append(next.Endpoints, EndpointEntry{Endpoint: ep, Name: name})
	}
	if err := SaveSettings(next); err != nil {
		return err
	}
	g.Settings = next
	return nil
}

// Bridge returns the configured bridge with id, and whether one exists.
func (g *Global) Bridge(id string) (Bridge, bool) {
	for _, p := range g.Settings.Bridges {
		if p.ID == id {
			return p, true
		}
	}
	return Bridge{}, false
}

// RecordLaunch stamps s with the active endpoint, saves it and keeps it as
// LastLaunch.
func (g *Global) RecordLaunch(s LaunchState) error {
	ep := g.ActiveEndpoint()
	s.LastEndpointURL = ep.URL()
	if ep, ok := ep.(BridgeEndpoint); ok {
		s.LastBridgeID = ep.BridgeID()
	}
	g.LastLaunch = s
	return SaveState(s)
}

// Provider returns the ProviderInfo for id, or a zero value and false when
// g.Providers has no such provider.
func (g *Global) Provider(id string) (ProviderInfo, bool) {
	for _, p := range g.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderInfo{}, false
}
