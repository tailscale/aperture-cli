package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ClientConfigDir returns the directory where a client may store its own
// isolated state. The directory is created if it does not exist. Typical
// usage: clients that manage their own on-disk home (e.g. Codex's CODEX_HOME,
// Gemini's GEMINI_CLI_HOME) pass the returned path to the agent binary.
func ClientConfigDir(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "aperture", "clients", name)
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}

// TypedStore is a JSON file holding a single value of type T. Each call to
// Load/Save round-trips through disk; the store holds no in-memory cache.
type TypedStore[T any] struct {
	path string
}

// ClientConfig returns a typed JSON store at
// <UserConfigDir>/aperture/clients/<name>.json. The file is created on first
// Save. Load returns a zero T if the file does not exist.
func ClientConfig[T any](name string) (*TypedStore[T], error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return &TypedStore[T]{
		path: filepath.Join(dir, "aperture", "clients", name+".json"),
	}, nil
}

// Load reads the stored value. Returns a zero T if the file is missing or
// unreadable.
func (s *TypedStore[T]) Load() (T, error) {
	var zero T
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return zero, nil
		}
		return zero, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, err
	}
	return v, nil
}

// Save persists the given value. Creates the parent directory if needed.
func (s *TypedStore[T]) Save(v T) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

// Path returns the absolute path to the config file.
func (s *TypedStore[T]) Path() string { return s.path }
