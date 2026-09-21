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
	"slices"
	"strconv"
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

// BridgeStateDir returns the tsnet state directory for one slot of a bridge.
// Every concurrent aperture process running the bridge claims its own slot,
// because one directory is one node key and the control plane hands the node
// to whichever process registered last. Slot 1 keeps the path existing
// bridges already have; further slots get a numeric sibling.
func BridgeStateDir(id string, slot int) (string, error) {
	if slot < 1 {
		return "", fmt.Errorf("slot %d: slots number from 1", slot)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	suffix := strings.TrimPrefix(id, "bridge-")
	if suffix == "" {
		return "", fmt.Errorf("bridge ID is empty")
	}
	base := filepath.Join(dir, "aperture", "bridges", suffix)
	if slot == 1 {
		return base, nil
	}
	return fmt.Sprintf("%s-%d", base, slot), nil
}

// BridgeStateSlots returns the numbers of the bridge's slots that have a
// state directory on disk, in order. Removal walks it: every slot is a node
// the bridge registered, and each needs its own logout.
func BridgeStateSlots(id string) ([]int, error) {
	base, err := BridgeStateDir(id, 1)
	if err != nil {
		return nil, err
	}
	var slots []int
	if isDir(base) {
		slots = append(slots, 1)
	}
	matches, err := filepath.Glob(base + "-*")
	if err != nil {
		return nil, err
	}
	for _, match := range matches {
		slot, err := strconv.Atoi(strings.TrimPrefix(match, base+"-"))
		if err != nil || slot < 2 || !isDir(match) {
			continue
		}
		slots = append(slots, slot)
	}
	slices.Sort(slots)
	return slots, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
