// Package config holds the launcher's app-level persistent state: the list
// of Aperture endpoints the user has configured, the active endpoint, the
// YOLO-mode flag, and the record of the last-used client launch. Clients
// also reach through this package for isolated per-client JSON storage.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultLocation is the fallback Aperture endpoint URL used when the user
// has no saved settings.
const DefaultLocation = "http://ai"

// Endpoint holds the URL and per-endpoint configuration for an Aperture proxy.
type Endpoint struct {
	URL      string `json:"url"`
	PortalID string `json:"portalId,omitempty"`
}

// Portal is an embedded tsnet node used to reach Aperture without requiring
// Tailscale to run on the host.
type Portal struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Settings holds persistent launcher configuration managed by the user.
type Settings struct {
	// Portals is the set of embedded tsnet nodes the user has configured.
	Portals []Portal `json:"portals,omitempty"`

	// Endpoints is the ordered list of Aperture proxy endpoints.
	// The first entry is used as the active endpoint on startup.
	Endpoints []Endpoint `json:"endpoints,omitempty"`

	// YoloMode appends each client's skip-permissions args (e.g.
	// --dangerously-skip-permissions for Claude Code, --yolo for Gemini)
	// when launching an agent.
	YoloMode bool `json:"yoloMode,omitempty"`
}

// settingsPath returns the path to the launcher settings JSON file.
func settingsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aperture", "settings.json"), nil
}

// LoadSettings reads the persisted launcher settings. Errors are silently
// ignored and a default Settings value is returned.
func LoadSettings() (Settings, error) {
	path, err := settingsPath()
	if err != nil {
		return defaultSettings(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultSettings(), nil
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return defaultSettings(), nil
	}
	if len(s.Endpoints) == 0 {
		s.Endpoints = []Endpoint{{URL: DefaultLocation}}
	}
	return s, nil
}

// SaveSettings persists the launcher settings to disk.
func SaveSettings(s Settings) error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func defaultSettings() Settings {
	return Settings{
		Endpoints: []Endpoint{{URL: DefaultLocation}},
	}
}

// PortalStateDir returns the tsnet state directory for a portal ID.
func PortalStateDir(id string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	suffix := strings.TrimPrefix(id, "portal-")
	if suffix == "" {
		return "", fmt.Errorf("portal ID is empty")
	}
	return filepath.Join(dir, "aperture", "portals", suffix), nil
}

func sameEndpoint(a, b Endpoint) bool {
	return a.URL == b.URL && a.PortalID == b.PortalID
}

func newPortalID(existing []Portal) (string, error) {
	for range 10 {
		var b [3]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		id := "portal-" + hex.EncodeToString(b[:])
		found := false
		for _, p := range existing {
			if p.ID == id {
				found = true
				break
			}
		}
		if !found {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique portal ID")
}
