package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/tailscale/aperture-cli/internal/config"
)

type opencodeConfig struct {
	Schema   string                      `json:"$schema,omitempty"`
	Provider map[string]opencodeProvider `json:"provider,omitempty"`
}

type opencodeProvider struct {
	NPM     string                        `json:"npm,omitempty"`
	Name    string                        `json:"name,omitempty"`
	Options map[string]string             `json:"options,omitempty"`
	Models  map[string]opencodeModelEntry `json:"models,omitempty"`
	// Whitelist limits the active model list to exactly these IDs. Without
	// it, OpenCode merges its built-in models.dev database entries on top of
	// our config (e.g. for provider IDs like "openai" or "anthropic").
	Whitelist []string `json:"whitelist,omitempty"`
}

type opencodeModelEntry struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// pickSDK chooses the AI SDK npm package and baseline options for a provider
// based on its compatibility map. Order matters: when a provider supports
// multiple protocols, the first match wins.
func pickSDK(compat map[string]bool, apertureHost string) (npm string, options map[string]string) {
	switch {
	case compat["openai_responses"]:
		return "@ai-sdk/openai", map[string]string{
			"baseURL": apertureHost + "/v1",
			"apiKey":  "not-required",
		}
	case compat["anthropic_messages"]:
		return "@ai-sdk/anthropic", map[string]string{
			"baseURL": apertureHost + "/v1",
			"apiKey":  "not-required",
		}
	case compat["openai_chat"]:
		return "@ai-sdk/openai-compatible", map[string]string{
			"baseURL": apertureHost + "/v1",
			"apiKey":  "not-required",
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
	case compat["bedrock_model_invoke"] || compat["bedrock_converse"]:
		return "@ai-sdk/amazon-bedrock", map[string]string{
			"region":   "us-east-1",
			"endpoint": apertureHost + "/bedrock",
		}
	case compat["gemini_generate_content"]:
		return "@ai-sdk/google", map[string]string{
			"baseURL": apertureHost + "/v1beta",
			"apiKey":  "not-required",
		}
	}
	return "", nil
}

// writeProviderConfig writes the per-launch OpenCode config under
// ~/.opencode/tmp_aperture_config.json and returns the path plus a cleanup
// function that removes the file. The config defines one provider (the
// chosen one) mapped to the SDK picked from its compatibility map.
func writeProviderConfig(apertureHost string, p config.ProviderInfo) (string, func(), error) {
	npm, options := pickSDK(p.Compatibility, apertureHost)

	models := make(map[string]opencodeModelEntry, len(p.Models))
	whitelist := make([]string, 0, len(p.Models))
	for _, m := range p.Models {
		fqn := p.ID + "/" + m
		models[fqn] = opencodeModelEntry{ID: m, Name: fqn}
		whitelist = append(whitelist, fqn)
	}

	cfg := opencodeConfig{
		Schema: "https://opencode.ai/config.json",
		Provider: map[string]opencodeProvider{
			p.ID: {
				NPM:       npm,
				Name:      "Aperture (" + p.ID + ")",
				Options:   options,
				Models:    models,
				Whitelist: whitelist,
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return "", nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, err
	}
	configDir := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", nil, err
	}
	path := filepath.Join(configDir, "tmp_aperture_config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", nil, err
	}
	return path, func() { os.Remove(path) }, nil
}
