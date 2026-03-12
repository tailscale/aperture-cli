package profiles

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
)

// BackendType identifies the upstream LLM provider.
type BackendType string

const (
	BackendAnthropic BackendType = "anthropic"
	BackendBedrock   BackendType = "bedrock"
	BackendVertex    BackendType = "vertex"
	BackendGemini    BackendType = "gemini"
	BackendOpenAI    BackendType = "openai"
)

// Backend is a selectable upstream destination.
type Backend struct {
	Type        BackendType
	DisplayName string
}

// Profile describes one AI coding agent.
type Profile interface {
	Name() string
	BinaryName() string
	SupportedBackends() []Backend
	Env(apertureHost string, b Backend) (map[string]string, error)
}

// ConfigWriter is implemented by profiles that need a temporary config file
// written before launch. envKey is the environment variable name to set to
// configPath. The returned cleanup func removes the file or directory.
type ConfigWriter interface {
	WriteConfig(apertureHost string, b Backend) (envKey, configPath string, cleanup func(), err error)
}

// YoloProfile is implemented by profiles that support a "skip permissions"
// flag. The returned args are appended to the command when YOLO mode is on.
type YoloProfile interface {
	YoloArgs() []string
}

// ProviderInfo mirrors the JSON response from GET /api/providers.
type ProviderInfo struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Models        []string        `json:"models"`
	Compatibility map[string]bool `json:"compatibility"`
}

// CompatChecker is implemented by profiles that declare which API
// compatibility keys they require for each backend. The TUI uses this
// to hide backends that no provider supports.
type CompatChecker interface {
	RequiredCompat(b Backend) []string
}

// ProviderEnvSetter is implemented by profiles that derive additional
// environment variables from provider metadata (e.g. model names).
type ProviderEnvSetter interface {
	ProviderEnv(b Backend, providers []ProviderInfo) map[string]string
}

// Combo is a resolved (profile, backend) pair.
type Combo struct {
	Profile Profile
	Backend Backend
}

// Manager holds all known profiles and resolves which are installed.
type Manager struct {
	profiles []Profile
}

// NewManager returns a Manager with all built-in profiles registered.
func NewManager() *Manager {
	return &Manager{
		profiles: []Profile{
			&ClaudeCodeProfile{},
			&GeminiCLIProfile{},
			&OpenCodeProfile{},
			&CodexProfile{},
		},
	}
}

// PathHinter is implemented by profiles that know common filesystem
// locations where their binary may be installed. These paths are checked
// as a fallback when the binary is not found on the current PATH (e.g.
// after a fresh install that updated shell profiles but the running
// process still has the old PATH).
type PathHinter interface {
	// CommonPaths returns absolute paths where the binary is commonly
	// installed. The returned paths should include the binary name
	// (e.g. "~/.local/bin/claude", not just "~/.local/bin").
	CommonPaths() []string
}

// commonBinDirs returns well-known user-local directories where binaries are
// commonly installed but may not be on PATH yet (e.g. after a fresh install
// that updated shell profiles but the running process still has the old PATH).
// System-wide directories like /usr/local/bin and /opt/homebrew/bin are
// intentionally excluded: binaries there will be found by exec.LookPath.
func commonBinDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
		filepath.Join(home, ".npm-global", "bin"),
	}
}

// IsInstalled reports whether the binary for a profile can be found,
// checking PATH first and then common installation directories.
func IsInstalled(p Profile) bool {
	if p.BinaryName() == "" {
		return true
	}
	return FindBinary(p) != ""
}

// FindBinary returns the resolved path to a profile's binary. It checks
// exec.LookPath (i.e. $PATH) first, then profile-specific common paths,
// then general well-known binary directories. Returns "" if not found.
func FindBinary(p Profile) string {
	name := p.BinaryName()
	if name == "" {
		return ""
	}

	// Fast path: binary is on the current PATH.
	if path, err := exec.LookPath(name); err == nil {
		return path
	}

	// Check profile-specific common installation paths.
	if ph, ok := p.(PathHinter); ok {
		for _, path := range ph.CommonPaths() {
			if isExecutable(path) {
				return path
			}
		}
	}

	// Check general well-known binary directories.
	for _, dir := range commonBinDirs() {
		path := filepath.Join(dir, name)
		if isExecutable(path) {
			return path
		}
	}

	return ""
}

// isExecutable reports whether the file at path exists and is executable.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	// On Unix, check that at least one execute bit is set.
	return !info.IsDir() && info.Mode()&0o111 != 0
}

// Installer is implemented by profiles that can provide installation
// instructions when their binary is not found on PATH.
type Installer interface {
	InstallHint() string
}

// Checker is implemented by profiles that need to validate the local
// environment before launching (e.g. checking config files).
type Checker interface {
	Check(b Backend) error
}

// Uninstaller is implemented by profiles that can provide uninstall support.
// UninstallHint returns a human-readable description shown before the user
// confirms. Uninstall returns the function that performs the actual removal.
type Uninstaller interface {
	UninstallHint() string
	Uninstall() func() error
}

// AllProfiles returns all registered profiles regardless of installation status.
func (m *Manager) AllProfiles() []Profile {
	return m.profiles
}

// FilteredBackends returns the backends for a profile filtered by provider
// compatibility. If providers is nil, no filtering is applied.
func (m *Manager) FilteredBackends(p Profile, providers []ProviderInfo) []Backend {
	if providers == nil {
		return p.SupportedBackends()
	}
	checker, ok := p.(CompatChecker)
	if !ok {
		return p.SupportedBackends()
	}
	var out []Backend
	for _, b := range p.SupportedBackends() {
		keys := checker.RequiredCompat(b)
		if anyProviderSupports(providers, keys) {
			out = append(out, b)
		}
	}
	return out
}

// anyProviderSupports reports whether at least one provider has any of the
// given compatibility keys set to true.
func anyProviderSupports(providers []ProviderInfo, keys []string) bool {
	for _, p := range providers {
		for _, k := range keys {
			if p.Compatibility[k] {
				return true
			}
		}
	}
	return false
}

// ValidCombos returns all (profile, backend) combos where the profile binary
// is present on PATH. If providers is non-nil, backends are filtered by
// provider compatibility.
func (m *Manager) ValidCombos(providers []ProviderInfo) []Combo {
	var combos []Combo
	for _, p := range m.profiles {
		if !IsInstalled(p) {
			continue
		}
		for _, b := range m.FilteredBackends(p, providers) {
			combos = append(combos, Combo{Profile: p, Backend: b})
		}
	}
	return combos
}

// InstalledProfiles returns only the profiles whose binary is on PATH.
func (m *Manager) InstalledProfiles() []Profile {
	var out []Profile
	for _, p := range m.profiles {
		if IsInstalled(p) {
			out = append(out, p)
		}
	}
	return out
}

// StateFile records the last-used profile/backend for quick re-launch.
type StateFile struct {
	LastProfileName string `json:"lastProfileName,omitempty"`
	LastBackendType string `json:"lastBackendType,omitempty"`
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
// and an empty StateFile is returned.
func LoadState() (StateFile, error) {
	path, err := statePath()
	if err != nil {
		return StateFile{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return StateFile{}, nil
	}
	var s StateFile
	if err := json.Unmarshal(data, &s); err != nil {
		return StateFile{}, nil
	}
	return s, nil
}

// SaveState persists the launcher state to disk.
func SaveState(s StateFile) error {
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
