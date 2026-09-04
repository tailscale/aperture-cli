package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

const testHost = "http://ai.example.com"

func TestInstallCommandDetectsPipelineFailures(t *testing.T) {
	plan := (&Client{}).Install(&config.Global{})
	cmd, err := plan.Run()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cmd.Args, []string{
		"bash", "-o", "pipefail", "-c",
		"curl -fsSL https://opencode.ai/install | bash",
	}) {
		t.Errorf("install command args = %q, want bash with pipefail", cmd.Args)
	}
}

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "anthropic", SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}},
		{ID: "openai", SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}},
		{ID: "bedrock", SupportedEndpoints: map[string]bool{config.EndpointBedrockConverse: true}},
		{ID: "none", SupportedEndpoints: map[string]bool{"/unknown": true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 3 {
		t.Errorf("compatibleProviders len = %d, want 3: %+v", len(got), got)
	}
}

func TestPickSDK(t *testing.T) {
	cases := []struct {
		name      string
		endpoints map[string]bool
		wantNPM   string
	}{
		{"responses", map[string]bool{config.EndpointOpenAIResponses: true}, "@ai-sdk/openai"},
		{"anthropic", map[string]bool{config.EndpointAnthropicMessages: true}, "@ai-sdk/anthropic"},
		{"chat_only", map[string]bool{config.EndpointOpenAIChat: true}, "@ai-sdk/openai-compatible"},
		{"vertex", map[string]bool{config.EndpointVertexGemini: true}, "@ai-sdk/google-vertex"},
		{"bedrock", map[string]bool{config.EndpointBedrockConverse: true}, "@ai-sdk/amazon-bedrock"},
		{"gemini", map[string]bool{config.EndpointGemini: true}, "@ai-sdk/google"},
		{"none", map[string]bool{"/unknown": true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			npm, _ := pickSDK(config.ProviderInfo{SupportedEndpoints: tc.endpoints}, testHost)
			if npm != tc.wantNPM {
				t.Errorf("npm = %q, want %q", npm, tc.wantNPM)
			}
		})
	}
}

func TestPickSDK_ResponsesBeatsChat(t *testing.T) {
	npm, _ := pickSDK(config.ProviderInfo{SupportedEndpoints: map[string]bool{
		config.EndpointOpenAIChat:      true,
		config.EndpointOpenAIResponses: true,
	}}, testHost)
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
		wantNPM     string
		wantOptions map[string]string
	}{
		{
			name: "anthropic",
			provider: config.ProviderInfo{
				ID: "anthropic", Name: "Anthropic",
				Models:             []string{"claude-sonnet-4-5", "claude-haiku-4-5"},
				SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true},
			},
			wantNPM: "@ai-sdk/anthropic",
			wantOptions: map[string]string{
				"baseURL": testHost + "/v1",
				"apiKey":  "not-required",
			},
		},
		{
			name: "bedrock",
			provider: config.ProviderInfo{
				ID: "bedrock", Name: "AWS Bedrock",
				Models:             []string{"us.anthropic.claude-opus-4-7"},
				SupportedEndpoints: map[string]bool{config.EndpointBedrockConverse: true},
			},
			wantNPM: "@ai-sdk/amazon-bedrock",
			wantOptions: map[string]string{
				"region":   "us-east-1",
				"endpoint": testHost + "/bedrock",
			},
		},
		{
			name: "vertex",
			provider: config.ProviderInfo{
				ID: "vertex", Name: "Vertex",
				Models: []string{"gemini-2.5-pro"},
				SupportedEndpoints: map[string]bool{
					config.EndpointVertexGemini: true,
					config.EndpointVertexClaude: true,
				},
			},
			wantNPM: "@ai-sdk/google-vertex",
			wantOptions: map[string]string{
				"apiKey":  "not-required",
				"baseURL": testHost + "/v1/projects/_aperture_auto_vertex_project_id_/locations/_aperture_auto_vertex_region_/publishers/google",
			},
		},
		{
			name: "openai",
			provider: config.ProviderInfo{
				ID: "openai", Name: "OpenAI",
				Models: []string{"gpt-5"},
				SupportedEndpoints: map[string]bool{
					config.EndpointOpenAIChat:      true,
					config.EndpointOpenAIResponses: true,
				},
			},
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
				Models:             []string{"qwen/qwen3-235b-a22b-2507"},
				SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true},
			},
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
			prov, ok := cfg.Provider[tt.provider.ID]
			if !ok {
				t.Fatalf("provider %q missing from config", tt.provider.ID)
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
				fqn := tt.provider.ID + "/" + m
				entry, ok := prov.Models[fqn]
				if !ok {
					t.Errorf("model %q missing from config", fqn)
					continue
				}
				if entry["id"] != m {
					t.Errorf("model %q id = %q, want %q", fqn, entry["id"], m)
				}
			}

			cleanup()
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Errorf("config file still exists after cleanup")
			}
		})
	}
}
