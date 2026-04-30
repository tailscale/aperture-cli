package profiles

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
)

// OpenCodeProfile implements Profile for the `opencode` CLI tool.
type OpenCodeProfile struct {
}

func (o *OpenCodeProfile) Name() string { return "OpenCode" }

func (o *OpenCodeProfile) BinaryName() string { return "opencode" }

func (o *OpenCodeProfile) CommonPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".opencode", "bin", "opencode"),
		filepath.Join(home, ".local", "bin", "opencode"),
	}
}

func (o *OpenCodeProfile) InstallHint() string {
	return "curl -fsSL https://opencode.ai/install | bash"
}

func (o *OpenCodeProfile) UninstallHint() string {
	return "opencode uninstall --force\nrm -rf ~/.opencode/bin"
}

func (o *OpenCodeProfile) Uninstall() func() error {
	return func() error {
		if err := exec.Command("opencode", "uninstall", "--force").Run(); err != nil {
			return err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return os.RemoveAll(filepath.Join(home, ".opencode", "bin"))
	}
}

// openCodeBackend is the single abstract backend OpenCode advertises. The
// real routing is decided per-provider from its compatibility map.
var openCodeBackend = Backend{Type: BackendOpenAI, DisplayName: "OpenCode"}

func (o *OpenCodeProfile) SupportedBackends() []Backend {
	return []Backend{openCodeBackend}
}

// RequiredCompat accepts any provider that speaks one of the protocols we can
// translate into an OpenCode config.
func (o *OpenCodeProfile) RequiredCompat(Backend) []string {
	return []string{
		"anthropic_messages",
		"bedrock_converse",
		"google_generate_content",
		"google_raw_predict",
		"openai_responses",
		"openai_chat",
	}
}

// Env returns backend-agnostic env vars. Provider-specific env vars (AWS,
// Google Vertex magic strings) are set via ProviderEnv.
func (o *OpenCodeProfile) Env(string, Backend) (map[string]string, error) {
	return map[string]string{}, nil
}

// ProviderEnv sets env vars that depend on the chosen provider's protocol.
func (o *OpenCodeProfile) ProviderEnv(_ Backend, providers []ProviderInfo) map[string]string {
	if len(providers) == 0 {
		return nil
	}
	p := providers[0]
	if p.Compatibility["bedrock_converse"] {
		return map[string]string{
			"AWS_ACCESS_KEY_ID":     "not-needed",
			"AWS_SECRET_ACCESS_KEY": "not-needed",
			"AWS_REGION":            "us-east-1",
		}
	}
	return nil
}

type opencodeConfig struct {
	Schema   string                      `json:"$schema,omitempty"`
	Provider map[string]opencodeProvider `json:"provider,omitempty"`
}

type opencodeProvider struct {
	NPM     string                        `json:"npm,omitempty"`
	Name    string                        `json:"name,omitempty"`
	Options map[string]string             `json:"options,omitempty"`
	Models  map[string]opencodeModelEntry `json:"models,omitempty"`
}

type opencodeModelEntry struct {
	Name string `json:"name,omitempty"`
}

// pickOpenCodeSDK chooses the AI SDK npm package and baseline options for a
// provider based on its compatibility map. Priority is ordered so that
// protocols with richer native support win over OpenAI-compatible fallback.
func pickOpenCodeSDK(compat map[string]bool, apertureHost string) (npm string, options map[string]string) {
	switch {
	case compat["anthropic_messages"]:
		return "@ai-sdk/anthropic", map[string]string{
			"baseURL": apertureHost + "/v1",
			"apiKey":  "not-required",
		}
	case compat["bedrock_converse"]:
		return "@ai-sdk/amazon-bedrock", map[string]string{
			"region":   "us-east-1",
			"endpoint": apertureHost + "/bedrock",
		}
	case compat["google_generate_content"] || compat["google_raw_predict"]:
		// Setting apiKey triggers the Vertex SDK's "express mode" which skips
		// google-auth-library / ADC. We still need the full project-scoped
		// path because aperture's vertex router only matches that pattern;
		// the magic _aperture_auto_*_ placeholders are rewritten upstream.
		return "@ai-sdk/google-vertex", map[string]string{
			"apiKey":  "not-required",
			"baseURL": apertureHost + "/v1/projects/_aperture_auto_vertex_project_id_/locations/_aperture_auto_vertex_region_/publishers/google",
		}
	case compat["openai_responses"]:
		return "@ai-sdk/openai", map[string]string{
			"baseURL": apertureHost + "/v1",
			"apiKey":  "not-required",
		}
	case compat["openai_chat"]:
		return "@ai-sdk/openai-compatible", map[string]string{
			"baseURL": apertureHost + "/v1",
			"apiKey":  "not-required",
		}
	}
	return "", nil
}

func (o *OpenCodeProfile) WriteProviderConfig(apertureHost string, _ Backend, p ProviderInfo) (string, string, func(), error) {
	npm, options := pickOpenCodeSDK(p.Compatibility, apertureHost)

	models := make(map[string]opencodeModelEntry, len(p.Models))
	for _, m := range p.Models {
		models[m] = opencodeModelEntry{Name: m}
	}

	cfg := opencodeConfig{
		Schema: "https://opencode.ai/config.json",
		Provider: map[string]opencodeProvider{
			p.ID: {
				NPM:     npm,
				Name:    "Aperture (" + p.ID + ")",
				Options: options,
				Models:  models,
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return "", "", nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", nil, err
	}
	configDir := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", "", nil, err
	}
	path := filepath.Join(configDir, "tmp_aperture_config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", nil, err
	}
	return "OPENCODE_CONFIG", path, func() { os.Remove(path) }, nil
}
