package config

import (
	"fmt"
	"net/url"
	"strings"
)

// DefaultLocation is the well-known Aperture location. It is the first
// candidate every connection attempt tries, direct or bridged, and the
// fallback when the user has no saved settings.
const DefaultLocation = "http://ai"

// Endpoint holds the URL and per-endpoint configuration for an Aperture proxy.
type Endpoint struct {
	URL      string `json:"url"`
	BridgeID string `json:"bridgeId,omitempty"`
}

// Bridge is an embedded tsnet node used to reach Aperture without requiring
// Tailscale to run on the host.
type Bridge struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Tailnet is the network the node logged in to, recorded after a
	// successful connection so the connection picker can say which tailnet a
	// bridge reaches before it is started again.
	Tailnet string `json:"tailnet,omitempty"`
}

// ParseEndpoint turns user input into an Endpoint reached over bridgeID, which
// is empty for a direct connection. A bare host is assumed to be http, since
// Aperture is reached over the tailnet.
func ParseEndpoint(value, bridgeID string) (Endpoint, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return Endpoint{}, fmt.Errorf("endpoint URL must be an absolute http or https URL")
	}
	return Endpoint{URL: strings.TrimRight(value, "/"), BridgeID: bridgeID}, nil
}

func sameEndpoint(a, b Endpoint) bool {
	return a.URL == b.URL && a.BridgeID == b.BridgeID
}
