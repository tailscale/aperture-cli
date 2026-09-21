package config

import (
	"fmt"
	"strings"
)

// Startup is what the invocation asked the launcher to open. The zero value
// means it asked for nothing.
type Startup struct {
	URL        string
	BridgeName string
}

// Resolve returns the endpoint to open on, falling back to the saved active
// one when the invocation named nothing.
//
// A URL alone is a direct connection. A bridge alone opens at DefaultLocation,
// the same guess the connection picker makes, because naming a bridge usually
// means knowing how to get on the tailnet rather than what is listening on it.
// Both together pin the URL behind the bridge.
//
// Callers resolve before the TUI takes the terminal, so a URL we cannot use is
// a line on stderr rather than a full-screen error.
func (s Startup) Resolve(g *Global) (Endpoint, error) {
	url := strings.TrimSpace(s.URL)
	name := strings.TrimSpace(s.BridgeName)
	if url == "" && name == "" {
		return g.ActiveEndpoint(), nil
	}
	// The URL is checked before the bridge is looked up, because the lookup
	// writes: an invocation that exits with a usage error must not leave a
	// bridge on disk that the user then has to find and delete.
	location := DefaultLocation
	if url != "" {
		parsed, err := ParseEndpointURL(url)
		if err != nil {
			return nil, err
		}
		location = parsed
	}
	if name == "" {
		return Direct(location), nil
	}
	bridge, err := s.bridge(g, name)
	if err != nil {
		return nil, err
	}
	return Bridged(location, bridge.ID), nil
}

// bridge creates the named bridge if there is none, which is what makes a first
// run scriptable. Matching ignores case: the name is the user's own label and
// nothing keys off it.
//
// Two bridges can carry one name, and the flag then names neither: picking the
// first leaves the other unreachable from the command line, silently.
func (s Startup) bridge(g *Global, name string) (Bridge, error) {
	var matched []Bridge
	for _, b := range g.Settings.Bridges {
		if strings.EqualFold(b.Name, name) {
			matched = append(matched, b)
		}
	}
	switch len(matched) {
	case 0:
		return g.AddBridge(name)
	case 1:
		return matched[0], nil
	}
	ids := make([]string, 0, len(matched))
	for _, b := range matched {
		ids = append(ids, b.ID)
	}
	return Bridge{}, fmt.Errorf("%d bridges are called %q (%s); rename one in the connection picker", len(matched), name, strings.Join(ids, ", "))
}
