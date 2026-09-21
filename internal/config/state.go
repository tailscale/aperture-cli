package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LaunchState records the last client, endpoint, provider, backend and model
// used, so the TUI can offer a one-key relaunch on startup.
type LaunchState struct {
	LastClientName  string `json:"lastClientName,omitempty"`
	LastBackendType string `json:"lastBackendType,omitempty"`
	LastProviderID  string `json:"lastProviderId,omitempty"`
	LastModel       string `json:"lastModel,omitempty"`
	LastEndpointURL string `json:"lastEndpointUrl,omitempty"`
	LastBridgeID    string `json:"lastBridgeId,omitempty"`
}

// statePath returns the path to the launcher state JSON file.
func statePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aperture", "launcher.json"), nil
}

// LoadState reads the saved launcher state. Every error yields a zero
// LaunchState and no error: a lost relaunch hint is not worth refusing to
// start.
func LoadState() (LaunchState, error) {
	path, err := statePath()
	if err != nil {
		return LaunchState{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LaunchState{}, nil
	}
	var s LaunchState
	if err := json.Unmarshal(data, &s); err != nil {
		// Earlier launcher versions named the field lastProfileName. Read
		// that schema when the current one does not parse.
		var legacy struct {
			LastProfileName string `json:"lastProfileName,omitempty"`
			LastBackendType string `json:"lastBackendType,omitempty"`
			LastProviderID  string `json:"lastProviderId,omitempty"`
			LastModel       string `json:"lastModel,omitempty"`
		}
		if err := json.Unmarshal(data, &legacy); err != nil {
			return LaunchState{}, nil
		}
		s = LaunchState{
			LastClientName:  legacy.LastProfileName,
			LastBackendType: legacy.LastBackendType,
			LastProviderID:  legacy.LastProviderID,
			LastModel:       legacy.LastModel,
		}
	}
	// An old file may parse as the current schema and still carry the
	// client name only under lastProfileName.
	if s.LastClientName == "" {
		var legacy struct {
			LastProfileName string `json:"lastProfileName,omitempty"`
		}
		if err := json.Unmarshal(data, &legacy); err == nil && legacy.LastProfileName != "" {
			s.LastClientName = legacy.LastProfileName
		}
	}
	return s, nil
}

// SaveState persists the launcher state to disk.
func SaveState(s LaunchState) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
