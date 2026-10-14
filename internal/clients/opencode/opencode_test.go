package opencode

import (
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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

// The headless launch (ADR 0007): flags win, LastLaunch supplies the
// provider, and OpenCode picks its own model unless --model says otherwise.
func TestSpec(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	embed := config.ProviderInfo{ID: "embed", Models: []string{"embed-1"}, SupportedEndpoints: map[string]bool{"/v1/embeddings": true}}
	openai := config.ProviderInfo{ID: "openai", Models: []string{"gpt-5", "gpt-5-mini"}, SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true}}
	anthropic := config.ProviderInfo{ID: "anthropic", Models: []string{"claude"}, SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}}
	newGlobal := func() *config.Global {
		return &config.Global{ApertureHost: testHost, Providers: []config.ProviderInfo{embed, openai, anthropic}}
	}
	c := &Client{}

	// Defaults: the first compatible provider and no model, because OpenCode
	// picks its model in its own UI. The incompatible provider ahead of it
	// is skipped.
	g := newGlobal()
	spec, err := c.Spec(g, "", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if len(spec.Args) != 0 {
		t.Errorf("default args = %q, want none", spec.Args)
	}
	if g.LastLaunch.LastProviderID != "openai" {
		t.Errorf("provider = %q, want the first compatible one", g.LastLaunch.LastProviderID)
	}
	if spec.Env["OPENCODE_CONFIG"] == "" {
		t.Errorf("default env = %v, want OPENCODE_CONFIG set", spec.Env)
	}

	// Flags win, a --model passes through verbatim, and passthrough args
	// land after aperture's own.
	g = newGlobal()
	spec, err = c.Spec(g, "anthropic", "anthropic/anthropic/claude", []string{"--session", "s1"})
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if want := []string{"--model", "anthropic/anthropic/claude", "--session", "s1"}; !slices.Equal(spec.Args, want) {
		t.Errorf("flag args = %q, want %q", spec.Args, want)
	}
	if g.LastLaunch.LastProviderID != "anthropic" {
		t.Errorf("provider = %q, want the flag's provider", g.LastLaunch.LastProviderID)
	}

	// LastLaunch supplies the provider while it still fits, and no model:
	// the menu records none and headless replay invents none.
	g = newGlobal()
	g.LastLaunch = config.LaunchState{LastClientName: name, LastProviderID: "openai"}
	spec, err = c.Spec(g, "", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if len(spec.Args) != 0 {
		t.Errorf("replay args = %q, want none", spec.Args)
	}

	// LastLaunch for a provider that is gone or no longer compatible is
	// ignored in favor of the default.
	for _, stale := range []string{"ghost", "embed"} {
		g := newGlobal()
		g.LastLaunch = config.LaunchState{LastClientName: name, LastProviderID: stale}
		if _, err := c.Spec(g, "", "", nil); err != nil {
			t.Fatalf("Spec with stale %q: %v", stale, err)
		}
		if g.LastLaunch.LastProviderID != "openai" {
			t.Errorf("stale %q provider = %q, want the default", stale, g.LastLaunch.LastProviderID)
		}
	}

	// Errors: an unknown provider names the compatible ids, an
	// incompatible one says why, and no compatible provider says so.
	if _, err := c.Spec(newGlobal(), "ghost", "", nil); err == nil || !strings.Contains(err.Error(), "openai, anthropic") {
		t.Errorf("unknown provider: %v, want the compatible ids in the error", err)
	}
	if _, err := c.Spec(newGlobal(), "embed", "", nil); err == nil || !strings.Contains(err.Error(), "OpenCode can use") {
		t.Errorf("incompatible provider: %v, want the reason in the error", err)
	}
	none := &config.Global{ApertureHost: testHost, Providers: []config.ProviderInfo{embed}}
	if _, err := c.Spec(none, "", "", nil); err == nil || !strings.Contains(err.Error(), "OpenCode protocol") {
		t.Errorf("no compatible provider: %v, want the protocol named", err)
	}
	// The menu launch names no model and no extra args.
	spec, err = c.buildSpec(newGlobal(), openai, "", nil)
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if len(spec.Args) != 0 {
		t.Errorf("menu args = %q, want none", spec.Args)
	}
}

// Two concurrent launches must not share a per-launch config file: the
// second write would replace the first, and the first child to exit would
// delete it under the second.
func TestWriteProviderConfigIsUniquePerLaunch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	provs := config.ProviderInfo{ID: "openai", Models: []string{"gpt-5"}, SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true}}

	first, cleanupFirst, err := writeProviderConfig(testHost, provs)
	if err != nil {
		t.Fatalf("first writeProviderConfig: %v", err)
	}
	second, cleanupSecond, err := writeProviderConfig(testHost, provs)
	if err != nil {
		cleanupFirst()
		t.Fatalf("second writeProviderConfig: %v", err)
	}
	defer cleanupSecond()
	cleanupFirst()
	if first == second {
		t.Fatalf("both launches wrote %q", first)
	}
	if _, err := os.Stat(second); err != nil {
		t.Errorf("the second launch's config is gone: %v", err)
	}
}

// A write that fails partway through (disk full, quota) must not leave the
// partially written temp file behind in ~/.opencode.
func TestWriteProviderConfigRemovesTempFileOnWriteFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	provs := config.ProviderInfo{ID: "openai", Models: []string{"gpt-5"}, SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true}}

	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	var orig syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &orig); err != nil {
		t.Fatal(err)
	}
	defer syscall.Setrlimit(syscall.RLIMIT_FSIZE, &orig)
	limited := syscall.Rlimit{Cur: 1, Max: orig.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}

	if _, _, err := writeProviderConfig(testHost, provs); err == nil {
		t.Fatal("writeProviderConfig: want a write failure under the file-size limit")
	}

	entries, err := os.ReadDir(filepath.Join(home, ".opencode"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tmp_aperture_config_") {
			t.Errorf("leaked temp file %s after a write failure", e.Name())
		}
	}
}
