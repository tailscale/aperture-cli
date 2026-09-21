// Package config holds the launcher's persistent state: the Aperture
// endpoints the user has configured, the active endpoint, the YOLO-mode flag
// and the record of the last client launch. Clients also use this package
// for isolated per-client JSON storage.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tailscale.com/atomicfile"
)

// Settings holds the launcher configuration the user manages.
type Settings struct {
	// Bridges lists the embedded tsnet nodes the user has configured.
	Bridges []Bridge `json:"bridges,omitempty"`

	// Endpoints lists the Aperture endpoints in order. The first entry is
	// the active endpoint on startup.
	Endpoints endpointList `json:"endpoints,omitempty"`

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

// LoadSettings reads the saved launcher settings. A missing file means a
// first run and yields the defaults. Other read and parse errors are
// returned, so a later settings write cannot silently replace configuration
// that could not be read.
func LoadSettings() (Settings, error) {
	path, err := settingsPath()
	if err != nil {
		return Settings{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultSettings(), nil
		}
		return Settings{}, fmt.Errorf("reading settings: %w", err)
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("parsing settings: %w", err)
	}
	if len(s.Endpoints) == 0 {
		s.Endpoints = []Endpoint{Direct(DefaultLocation)}
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
	return atomicfile.WriteFile(path, data, 0o600)
}

func defaultSettings() Settings {
	return Settings{
		Endpoints: []Endpoint{Direct(DefaultLocation)},
	}
}

// BridgeStateDir returns the tsnet state directory for a bridge ID.
func BridgeStateDir(id string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	suffix := strings.TrimPrefix(id, "bridge-")
	if suffix == "" {
		return "", fmt.Errorf("bridge ID is empty")
	}
	return filepath.Join(dir, "aperture", "bridges", suffix), nil
}

func newBridgeID(existing []Bridge) (string, error) {
	for range 10 {
		var b [3]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		id := "bridge-" + hex.EncodeToString(b[:])
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
	return "", fmt.Errorf("could not generate a unique bridge ID")
}
