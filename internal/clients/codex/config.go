package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// writeConfig creates (or refreshes) the persistent CODEX_HOME directory
// holding auth.json and config.toml. Returns the directory path suitable
// for the CODEX_HOME environment variable.
//
// auth.json is pre-populated so Codex's first-run login prompt is skipped.
// config.toml pins the model provider to "aperture" pointing at the current
// aperture gateway.
//
// The path is the legacy "<config>/aperture/codex-home" used before the
// clients refactor, preserved so any per-home state Codex has stored under
// it continues to resolve.
func writeConfig(apertureHost string) (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	codexHome := filepath.Join(cfgDir, "aperture", "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return "", err
	}

	auth := map[string]any{
		"auth_mode":      "apikey",
		"OPENAI_API_KEY": "not-needed",
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), data, 0o600); err != nil {
		return "", err
	}

	baseURL := apertureHost + "/v1"
	cfg := "model_provider = \"aperture\"\n\n" +
		"[model_providers.aperture]\n" +
		"name = \"Aperture\"\n" +
		"base_url = " + strconv.Quote(baseURL) + "\n" +
		"env_key = \"OPENAI_API_KEY\"\n" +
		"supports_websockets = false\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(cfg), 0o600); err != nil {
		return "", err
	}

	return codexHome, nil
}
