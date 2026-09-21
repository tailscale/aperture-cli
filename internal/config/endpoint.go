package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// DefaultLocation is the well-known Aperture URL. Every connection attempt,
// direct or bridged, tries it first. A user with no saved settings starts
// here.
const DefaultLocation = "http://ai"

// Endpoint names a remote Aperture and how to reach it. Two kinds exist and
// no third. A DirectEndpoint is reached by the host itself. A BridgeEndpoint
// is reached through a Bridge's Machine. Values are comparable, so two
// Endpoints are the same when == says so.
type Endpoint interface {
	URL() string
	// WithURL returns the same kind of Endpoint pointed at a different URL.
	// An edit or an inline override keeps its Bridge.
	WithURL(url string) Endpoint
	endpoint()
}

// DirectEndpoint reaches an Aperture over the host's own network.
type DirectEndpoint struct{ url string }

// BridgeEndpoint reaches an Aperture through the Machine of one Bridge.
type BridgeEndpoint struct{ url, bridgeID string }

// Direct returns the Endpoint that reaches url without a Bridge.
func Direct(url string) DirectEndpoint { return DirectEndpoint{url: url} }

// Bridged returns the Endpoint that reaches url through bridgeID.
func Bridged(url, bridgeID string) BridgeEndpoint {
	return BridgeEndpoint{url: url, bridgeID: bridgeID}
}

func (e DirectEndpoint) URL() string                 { return e.url }
func (e DirectEndpoint) WithURL(url string) Endpoint { return Direct(url) }
func (e DirectEndpoint) endpoint()                   {}

func (e BridgeEndpoint) URL() string                 { return e.url }
func (e BridgeEndpoint) BridgeID() string            { return e.bridgeID }
func (e BridgeEndpoint) WithURL(url string) Endpoint { return Bridged(url, e.bridgeID) }
func (e BridgeEndpoint) endpoint()                   {}

// Bridge configures an embedded tsnet node. The node reaches Aperture without
// Tailscale running on the host.
type Bridge struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Tailnet names the tailnet the node last logged in to. Commit records it
	// after a successful connection, so the connection picker can name the
	// tailnet before the node starts again.
	Tailnet string `json:"tailnet,omitempty"`
}

// ParseEndpointURL turns user input into an Endpoint URL. A bare host gets
// http, since Aperture is reached over the tailnet.
func ParseEndpointURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("endpoint URL must be an absolute http or https URL")
	}
	return strings.TrimRight(value, "/"), nil
}

// endpointRecord is the settings.json form of an Endpoint. Both kinds share
// one shape, and bridgeId being present tells them apart. The file predates
// the two types and is not changing under existing users.
type endpointRecord struct {
	URL      string `json:"url"`
	BridgeID string `json:"bridgeId,omitempty"`
}

func recordOf(ep Endpoint) endpointRecord {
	switch ep := ep.(type) {
	case BridgeEndpoint:
		return endpointRecord{URL: ep.URL(), BridgeID: ep.BridgeID()}
	case nil:
		return endpointRecord{}
	default:
		return endpointRecord{URL: ep.URL()}
	}
}

func (r endpointRecord) endpoint() Endpoint {
	if r.BridgeID != "" {
		return Bridged(r.URL, r.BridgeID)
	}
	return Direct(r.URL)
}

func (e DirectEndpoint) MarshalJSON() ([]byte, error) { return json.Marshal(recordOf(e)) }
func (e BridgeEndpoint) MarshalJSON() ([]byte, error) { return json.Marshal(recordOf(e)) }

// endpointList holds the Endpoints field of Settings. Its elements are an
// interface, and encoding/json cannot decode those without being told the
// concrete types.
type endpointList []Endpoint

func (l *endpointList) UnmarshalJSON(data []byte) error {
	var records []endpointRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return err
	}
	*l = make(endpointList, 0, len(records))
	for _, r := range records {
		*l = append(*l, r.endpoint())
	}
	return nil
}
