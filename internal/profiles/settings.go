package profiles

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const defaultLocation = "http://ai"

// Endpoint holds the URL and per-endpoint configuration for an Aperture proxy.
type Endpoint struct {
	URL string `json:"url"`
}

// Settings holds persistent launcher configuration managed by the user.
type Settings struct {
	// Endpoints is the ordered list of Aperture proxy endpoints.
	// The first entry is used as the active endpoint on startup.
	Endpoints []Endpoint `json:"endpoints,omitempty"`

	// YoloMode appends each profile's skip-permissions args (e.g.
	// --dangerously-skip-permissions for claude, -yolo for gemini) when
	// launching an agent.
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
		s.Endpoints = []Endpoint{{URL: defaultLocation}}
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
		Endpoints: []Endpoint{{URL: defaultLocation}},
	}
}
