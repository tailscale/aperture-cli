package profiles

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// CodexProfile implements Profile for the OpenAI `codex` CLI tool.
type CodexProfile struct{}

func (c *CodexProfile) Name() string { return "OpenAI Codex" }

func (c *CodexProfile) BinaryName() string { return "codex" }

func (c *CodexProfile) CommonPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin", "codex"),
	}
}

func (c *CodexProfile) SupportedBackends() []Backend {
	return []Backend{
		{Type: BackendOpenAI, DisplayName: "OpenAI Compatible"},
	}
}

func (c *CodexProfile) InstallHint() string {
	return "npm install -g @openai/codex"
}

func (c *CodexProfile) UninstallHint() string {
	return "npm uninstall -g @openai/codex"
}

func (c *CodexProfile) Uninstall() func() error {
	return func() error {
		return exec.Command("npm", "uninstall", "-g", "@openai/codex").Run()
	}
}

func (c *CodexProfile) YoloArgs() []string {
	return []string{"--full-auto"}
}

func (c *CodexProfile) RequiredCompat(b Backend) []string {
	switch b.Type {
	case BackendOpenAI:
		return []string{"openai_responses"}
	default:
		return nil
	}
}

func (c *CodexProfile) Env(apertureHost string, b Backend) (map[string]string, error) {
	switch b.Type {
	case BackendOpenAI:
		return map[string]string{
			"OPENAI_BASE_URL": apertureHost + "/v1",
			"OPENAI_API_KEY":  "not-needed",
		}, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q for Codex", b.Type)
	}
}

// WriteConfig creates a persistent CODEX_HOME with auth.json pre-populated
// so Codex does not prompt for interactive login on first run.
func (c *CodexProfile) WriteConfig(_ string, _ Backend) (envKey, configPath string, cleanup func(), err error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", "", nil, err
	}
	codexHome := filepath.Join(cfgDir, "aperture", "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return "", "", nil, err
	}

	auth := map[string]any{
		"auth_mode":      "apikey",
		"OPENAI_API_KEY": "not-needed",
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return "", "", nil, err
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), data, 0o600); err != nil {
		return "", "", nil, err
	}
	return "CODEX_HOME", codexHome, func() {}, nil
}
