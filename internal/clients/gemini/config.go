package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// writeConfig creates a persistent GEMINI_CLI_HOME whose
// <home>/.gemini/settings.json selects the auth type matching the chosen
// backend (vertex-ai vs gemini-api-key). Returns the home path to hand to
// the agent via the GEMINI_CLI_HOME env var.
//
// The path is the legacy "<config>/aperture/gemini-home" used before the
// clients refactor, preserved so users' existing OAuth credentials under
// <home>/.gemini/oauth_creds.json keep working.
func writeConfig(selectedAuthType string) (string, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	geminiHome := filepath.Join(cfgDir, "aperture", "gemini-home")
	geminiDir := filepath.Join(geminiHome, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o700); err != nil {
		return "", err
	}
	settings := map[string]any{
		"security": map[string]any{
			"auth": map[string]any{
				"selectedType": selectedAuthType,
			},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(geminiDir, "settings.json"), data, 0o600); err != nil {
		return "", err
	}
	return geminiHome, nil
}
