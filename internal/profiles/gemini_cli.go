package profiles

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// GeminiCLIProfile implements Profile for the `gemini` CLI tool.
type GeminiCLIProfile struct{}

func (g *GeminiCLIProfile) Name() string { return "Gemini CLI" }

func (g *GeminiCLIProfile) BinaryName() string { return "gemini" }

func (g *GeminiCLIProfile) CommonPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin", "gemini"),
	}
}

func (g *GeminiCLIProfile) SupportedBackends() []Backend {
	return []Backend{
		{Type: BackendVertex, DisplayName: "Google Vertex"},
		{Type: BackendGemini, DisplayName: "Gemini API"},
	}
}

func (g *GeminiCLIProfile) InstallHint() string {
	return "npm install -g @google/gemini-cli"
}

func (g *GeminiCLIProfile) UninstallHint() string {
	return "npm uninstall -g @google/gemini-cli"
}

func (g *GeminiCLIProfile) Uninstall() func() error {
	return func() error {
		return exec.Command("npm", "uninstall", "-g", "@google/gemini-cli").Run()
	}
}

func (g *GeminiCLIProfile) RequiredCompat(b Backend) []string {
	switch b.Type {
	case BackendVertex:
		return []string{"experimental_gemini_cli_vertex_compat"}
	case BackendGemini:
		return []string{"gemini_generate_content"}
	default:
		return nil
	}
}

func (g *GeminiCLIProfile) WriteConfig(_ string, b Backend) (envKey, configPath string, cleanup func(), err error) {
	var selectedType string
	switch b.Type {
	case BackendVertex:
		selectedType = "vertex-ai"
	case BackendGemini:
		selectedType = "gemini-api-key"
	default:
		return "", "", func() {}, nil
	}

	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return "", "", nil, err
	}
	geminiHome := filepath.Join(cfgDir, "aperture", "gemini-home")
	geminiDir := filepath.Join(geminiHome, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o700); err != nil {
		return "", "", nil, err
	}
	settings := map[string]any{
		"security": map[string]any{
			"auth": map[string]any{
				"selectedType": selectedType,
			},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", "", nil, err
	}
	if err := os.WriteFile(filepath.Join(geminiDir, "settings.json"), data, 0o600); err != nil {
		return "", "", nil, err
	}
	return "GEMINI_CLI_HOME", geminiHome, func() {}, nil
}

func (g *GeminiCLIProfile) YoloArgs() []string {
	return []string{"--yolo"}
}

func (g *GeminiCLIProfile) Env(apertureHost string, b Backend) (map[string]string, error) {
	switch b.Type {
	case BackendVertex:
		return map[string]string{
			"GOOGLE_VERTEX_BASE_URL": apertureHost,
			"GOOGLE_API_KEY":         "not-needed",
		}, nil
	case BackendGemini:
		return map[string]string{
			"GEMINI_API_KEY":  "not-needed",
			"GEMINI_BASE_URL": apertureHost,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q for Gemini CLI", b.Type)
	}
}
