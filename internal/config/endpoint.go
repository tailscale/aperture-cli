package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// DefaultLocation is the well-known Aperture location. It is the first
// candidate every connection attempt tries, direct or bridged, and the
// fallback when the user has no saved settings.
const DefaultLocation = "http://ai"

// Endpoint is a remote Aperture and the way to it. There are two kinds and no
// third: a DirectEndpoint the host reaches itself, and a BridgeEndpoint
// reached through a Bridge's Machine. Values are comparable, so two Endpoints
// are the same when == says so.
type Endpoint interface {
	URL() string
	// WithURL is the same way to a different Aperture: an edit or an inline
	// override keeps its Bridge.
	WithURL(url string) Endpoint
	endpoint()
}

// DirectEndpoint is an Aperture the host reaches over its own network.
type DirectEndpoint struct{ url string }

// BridgeEndpoint is an Aperture reached through the Machine of one Bridge.
type BridgeEndpoint struct{ url, bridgeID string }

// Direct is the Endpoint for an Aperture at url reached without a Bridge.
func Direct(url string) DirectEndpoint { return DirectEndpoint{url: url} }

// Bridged is the Endpoint for an Aperture at url reached through bridgeID.
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

// ParseEndpointURL turns user input into the URL an Endpoint is made from. A
// bare host is assumed to be http, since Aperture is reached over the tailnet.
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

// endpointRecord is how an Endpoint is written to settings.json: one shape for
// both kinds, the kind told by whether bridgeId is present. The file predates
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

// endpointList is the settings field: a list whose elements are an interface,
// which encoding/json cannot decode without being told the concrete types.
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
