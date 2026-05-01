package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/tailscale/aperture-cli/internal/config"
)

// writeConfig creates (or refreshes) the persistent CODEX_HOME directory
// holding auth.json and config.toml. Returns the directory path suitable
// for the CODEX_HOME environment variable.
//
// auth.json is pre-populated so Codex's first-run login prompt is skipped.
// config.toml pins the model provider to "aperture" pointing at the current
// aperture gateway.
func writeConfig(apertureHost string) (string, error) {
	codexHome, err := config.ClientConfigDir("codex")
	if err != nil {
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
