package config

import (
	"fmt"
	"strings"
)

// EndpointFromFlags returns the Endpoint the command line asked the launcher
// to open. When neither flag names anything it returns the saved active
// Endpoint.
//
// The endpoint value resolves name first: a value matching a saved endpoint
// name is that endpoint, and anything else parses as a URL. A URL alone
// means a direct connection. A bridge alone opens at DefaultLocation, the
// same guess the connection picker makes, because naming a bridge usually
// means knowing how to get on the tailnet rather than what is listening on
// it. Both together pin the URL behind the bridge. With a named endpoint
// the bridge flag may only confirm the bridge the endpoint already uses.
//
// Callers resolve the flags before the TUI takes the terminal, so a URL we
// cannot use becomes a line on stderr rather than a full-screen error.
func EndpointFromFlags(g *Global, rawEndpoint, bridgeName string) (Endpoint, error) {
	rawEndpoint = strings.TrimSpace(rawEndpoint)
	bridgeName = strings.TrimSpace(bridgeName)
	if rawEndpoint == "" && bridgeName == "" {
		return g.ActiveEndpoint(), nil
	}
	if named, ok := g.Settings.EndpointNamed(rawEndpoint); ok {
		return checkBridgeFlag(g, named, bridgeName)
	}
	// Check the URL before looking up the bridge, because the lookup writes.
	// An invocation that exits with a usage error must not leave a bridge on
	// disk that the user then has to find and delete.
	location := DefaultLocation
	if rawEndpoint != "" {
		parsed, err := ParseEndpointURL(rawEndpoint)
		if err != nil {
			return nil, err
		}
		location = parsed
	}
	if bridgeName == "" {
		return Direct(location), nil
	}
	bridge, err := bridgeNamed(g, bridgeName)
	if err != nil {
		return nil, err
	}
	return Bridged(location, bridge.ID), nil
}

// checkBridgeFlag verifies a bridge flag against a named endpoint. The flag
// may only confirm the bridge the endpoint already uses; anything else is a
// contradiction on the command line, not a new endpoint. No bridge is
// created here: a usage error must not write.
func checkBridgeFlag(g *Global, ep Endpoint, bridgeName string) (Endpoint, error) {
	if bridgeName == "" {
		return ep, nil
	}
	bridged, ok := ep.(BridgeEndpoint)
	if !ok {
		return nil, fmt.Errorf("the endpoint is direct; --bridge %q does not apply", bridgeName)
	}
	bridge, ok := g.Bridge(bridged.BridgeID())
	if !ok || !strings.EqualFold(bridge.Name, bridgeName) {
		return nil, fmt.Errorf("the endpoint does not go through bridge %q", bridgeName)
	}
	return ep, nil
}

// bridgeNamed returns the bridge called name, creating it when there is none.
// Creating it is what makes a first run scriptable. Matching ignores case.
// The name is the user's own label and nothing keys off it.
//
// Two bridges can carry one name, and then the flag names neither. Picking
// the first would leave the other unreachable from the command line,
// silently.
func bridgeNamed(g *Global, name string) (Bridge, error) {
	var matched []Bridge
	for _, b := range g.Settings.Bridges {
		if strings.EqualFold(b.Name, name) {
			matched = append(matched, b)
		}
	}
	switch len(matched) {
	case 0:
		return g.AddBridge(name, 0)
	case 1:
		return matched[0], nil
	}
	ids := make([]string, 0, len(matched))
	for _, b := range matched {
		ids = append(ids, b.ID)
	}
	return Bridge{}, fmt.Errorf("%d bridges are called %q (%s); rename one in the connection picker", len(matched), name, strings.Join(ids, ", "))
}
