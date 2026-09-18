package config

import "strings"

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
	var bridgeID string
	if name != "" {
		bridge, err := s.bridge(g, name)
		if err != nil {
			return Endpoint{}, err
		}
		bridgeID = bridge.ID
	}
	if url == "" {
		return Endpoint{URL: DefaultLocation, BridgeID: bridgeID}, nil
	}
	return ParseEndpoint(url, bridgeID)
}

// bridge creates the named bridge if there is none, which is what makes a first
// run scriptable. Matching ignores case: the name is the user's own label and
// nothing keys off it.
func (s Startup) bridge(g *Global, name string) (Bridge, error) {
	for _, b := range g.Settings.Bridges {
		if strings.EqualFold(b.Name, name) {
			return b, nil
		}
	}
	return g.AddBridge(name)
}
