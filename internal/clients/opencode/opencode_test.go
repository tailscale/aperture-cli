package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

const testHost = "http://ai.example.com"

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "anthropic", Compatibility: map[string]bool{"anthropic_messages": true}},
		{ID: "openai", Compatibility: map[string]bool{"openai_chat": true}},
		{ID: "bedrock", Compatibility: map[string]bool{"bedrock_converse": true}},
		{ID: "none", Compatibility: map[string]bool{"something_else": true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 3 {
		t.Errorf("compatibleProviders len = %d, want 3: %+v", len(got), got)
	}
}

func TestPickSDK(t *testing.T) {
	cases := []struct {
		name    string
		compat  map[string]bool
		wantNPM string
	}{
		{"responses", map[string]bool{"openai_responses": true}, "@ai-sdk/openai"},
		{"anthropic", map[string]bool{"anthropic_messages": true}, "@ai-sdk/anthropic"},
		{"chat_only", map[string]bool{"openai_chat": true}, "@ai-sdk/openai-compatible"},
		{"vertex", map[string]bool{"google_generate_content": true}, "@ai-sdk/google-vertex"},
		{"bedrock", map[string]bool{"bedrock_converse": true}, "@ai-sdk/amazon-bedrock"},
		{"gemini", map[string]bool{"gemini_generate_content": true}, "@ai-sdk/google"},
		{"none", map[string]bool{"unknown": true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			npm, _ := pickSDK(tc.compat, testHost)
			if npm != tc.wantNPM {
				t.Errorf("npm = %q, want %q", npm, tc.wantNPM)
			}
		})
	}
}

func TestPickSDK_ResponsesBeatsChat(t *testing.T) {
	npm, _ := pickSDK(map[string]bool{
		"openai_chat":      true,
		"openai_responses": true,
	}, testHost)
	if npm != "@ai-sdk/openai" {
		t.Errorf("npm = %q, want @ai-sdk/openai (responses should win)", npm)
	}
}

func TestWriteProviderConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	tests := []struct {
		name        string
		provider    config.ProviderInfo
		wantID      string
		wantNPM     string
		wantOptions map[string]string
	}{
		{
			name: "anthropic_messages",
			provider: config.ProviderInfo{
				ID: "anthropic", Name: "Anthropic",
				Models:        []string{"claude-sonnet-4-5", "claude-haiku-4-5"},
				Compatibility: map[string]bool{"anthropic_messages": true},
			},
			wantID:  "anthropic",
			wantNPM: "@ai-sdk/anthropic",
			wantOptions: map[string]string{
				"baseURL": testHost + "/v1",
				"apiKey":  "not-required",
			},
		},
		{
			name: "bedrock_converse",
			provider: config.ProviderInfo{
				ID: "bedrock", Name: "AWS Bedrock",
				Models:        []string{"us.anthropic.claude-opus-4-7"},
				Compatibility: map[string]bool{"bedrock_converse": true},
			},
			wantID:  "amazon-bedrock",
			wantNPM: "@ai-sdk/amazon-bedrock",
			wantOptions: map[string]string{
				"region":   "us-east-1",
				"endpoint": testHost + "/bedrock",
			},
		},
		{
			name: "google_generate_content",
			provider: config.ProviderInfo{
				ID: "vertex", Name: "Vertex",
				Models: []string{"gemini-2.5-pro"},
				Compatibility: map[string]bool{
					"google_generate_content": true,
					"google_raw_predict":      true,
				},
			},
			wantID:  "vertex",
			wantNPM: "@ai-sdk/google-vertex",
			wantOptions: map[string]string{
				"apiKey":  "not-required",
				"baseURL": testHost + "/v1/projects/_aperture_auto_vertex_project_id_/locations/_aperture_auto_vertex_region_/publishers/google",
			},
		},
		{
			name: "openai_responses",
			provider: config.ProviderInfo{
				ID: "openai", Name: "OpenAI",
				Models: []string{"gpt-5"},
				Compatibility: map[string]bool{
					"openai_chat":      true,
					"openai_responses": true,
				},
			},
			wantID:  "openai",
			wantNPM: "@ai-sdk/openai",
			wantOptions: map[string]string{
				"baseURL": testHost + "/v1",
				"apiKey":  "not-required",
			},
		},
		{
			name: "openai_chat_only",
			provider: config.ProviderInfo{
				ID: "openrouter", Name: "OpenRouter",
				Models:        []string{"qwen/qwen3-235b-a22b-2507"},
				Compatibility: map[string]bool{"openai_chat": true},
			},
			wantID:  "openrouter",
			wantNPM: "@ai-sdk/openai-compatible",
			wantOptions: map[string]string{
				"baseURL": testHost + "/v1",
				"apiKey":  "not-required",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath, cleanup, err := writeProviderConfig(testHost, tt.provider)
			if err != nil {
				t.Fatalf("writeProviderConfig: %v", err)
			}

			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("config file not readable: %v", err)
			}
			var cfg struct {
				Provider map[string]struct {
					NPM       string                       `json:"npm"`
					Name      string                       `json:"name"`
					Options   map[string]string            `json:"options"`
					Models    map[string]map[string]string `json:"models"`
					Whitelist []string                     `json:"whitelist"`
				} `json:"provider"`
			}
			if err := json.Unmarshal(data, &cfg); err != nil {
				t.Fatalf("json: %v", err)
			}
			prov, ok := cfg.Provider[tt.wantID]
			if !ok {
				t.Fatalf("provider %q missing from config", tt.wantID)
			}
			if prov.NPM != tt.wantNPM {
				t.Errorf("npm = %q, want %q", prov.NPM, tt.wantNPM)
			}
			wantName := "Aperture (" + tt.provider.ID + ")"
			if prov.Name != wantName {
				t.Errorf("name = %q, want %q", prov.Name, wantName)
			}
			for k, want := range tt.wantOptions {
				if got := prov.Options[k]; got != want {
					t.Errorf("options[%q] = %q, want %q", k, got, want)
				}
			}
			if len(prov.Models) != len(tt.provider.Models) {
				t.Errorf("models len = %d, want %d", len(prov.Models), len(tt.provider.Models))
			}
			for _, m := range tt.provider.Models {
				name := configuredModelName(tt.wantID, tt.provider.ID, m)
				entry, ok := prov.Models[name]
				if !ok {
					t.Errorf("model %q missing from config", name)
					continue
				}
				if entry["id"] != m {
					t.Errorf("model %q id = %q, want %q", name, entry["id"], m)
				}
			}

			cleanup()
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Errorf("config file still exists after cleanup")
			}
		})
	}
}
