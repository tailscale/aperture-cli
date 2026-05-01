package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LaunchState records the last-used client/backend/provider/model so the TUI
// can offer a one-key quick re-launch on startup.
type LaunchState struct {
	LastClientName  string `json:"lastClientName,omitempty"`
	LastBackendType string `json:"lastBackendType,omitempty"`
	LastProviderID  string `json:"lastProviderId,omitempty"`
	LastModel       string `json:"lastModel,omitempty"`
}

// statePath returns the path to the launcher state JSON file.
func statePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aperture", "launcher.json"), nil
}

// LoadState reads the persisted launcher state. Errors are silently ignored
// and a zero LaunchState is returned.
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
		// Fall back to the legacy schema used by earlier versions of the
		// launcher, which named the field lastProfileName.
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
	// Accept old-format files that only have lastProfileName set.
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
