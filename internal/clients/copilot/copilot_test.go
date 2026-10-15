package copilot

import (
	"slices"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

const testHost = "http://ai.example.com"

func TestBuildEnv_OpenAIChat(t *testing.T) {
	b := backends[0] // openai_chat
	env := buildEnv(testHost, b, "prov/gpt-5")

	want := map[string]string{
		"COPILOT_PROVIDER_BASE_URL": testHost + "/v1",
		"COPILOT_PROVIDER_TYPE":     "openai",
		"COPILOT_PROVIDER_API_KEY":  "not-needed",
		"COPILOT_OFFLINE":           "true",
		"COPILOT_PROVIDER_WIRE_API": "completions",
		"COPILOT_MODEL":             "gpt-5",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("env has %d keys, want %d", len(env), len(want))
	}
}

func TestBuildEnv_OpenAIResponses(t *testing.T) {
	b := backends[1] // openai_responses
	env := buildEnv(testHost, b, "")

	if env["COPILOT_PROVIDER_WIRE_API"] != "responses" {
		t.Errorf("WIRE_API = %q, want responses", env["COPILOT_PROVIDER_WIRE_API"])
	}
	if _, ok := env["COPILOT_MODEL"]; ok {
		t.Error("COPILOT_MODEL should not be set when model is empty")
	}
	if len(env) != 5 {
		t.Errorf("env has %d keys, want 5", len(env))
	}
}

func TestBuildEnv_Anthropic(t *testing.T) {
	b := backends[2] // anthropic
	env := buildEnv(testHost, b, "prov/claude-sonnet-4")

	if env["COPILOT_PROVIDER_BASE_URL"] != testHost {
		t.Errorf("BASE_URL = %q, want %q (no /v1 for anthropic)", env["COPILOT_PROVIDER_BASE_URL"], testHost)
	}
	if _, ok := env["COPILOT_PROVIDER_WIRE_API"]; ok {
		t.Error("WIRE_API should not be set for anthropic")
	}
	if env["COPILOT_MODEL"] != "claude-sonnet-4" {
		t.Errorf("COPILOT_MODEL = %q, want claude-sonnet-4", env["COPILOT_MODEL"])
	}
	if len(env) != 5 {
		t.Errorf("env has %d keys, want 5", len(env))
	}
}

func TestBackendsFor(t *testing.T) {
	t.Run("openai_both", func(t *testing.T) {
		p := config.ProviderInfo{SupportedEndpoints: map[string]bool{
			config.EndpointOpenAIChat:      true,
			config.EndpointOpenAIResponses: true,
		}}
		bs := backendsFor(p)
		if len(bs) != 2 {
			t.Errorf("backendsFor = %d, want 2", len(bs))
		}
	})
	t.Run("anthropic_only", func(t *testing.T) {
		p := config.ProviderInfo{SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}}
		bs := backendsFor(p)
		if len(bs) != 1 || bs[0].id != "anthropic" {
			t.Errorf("backendsFor = %+v, want [anthropic]", bs)
		}
	})
	t.Run("bedrock_none", func(t *testing.T) {
		p := config.ProviderInfo{SupportedEndpoints: map[string]bool{"/unknown": true}}
		bs := backendsFor(p)
		if len(bs) != 0 {
			t.Errorf("backendsFor = %+v, want empty", bs)
		}
	})
}

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "openai", SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true, config.EndpointOpenAIResponses: true}},
		{ID: "anthropic", SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}},
		{ID: "bedrock", SupportedEndpoints: map[string]bool{config.EndpointBedrockConverse: true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 2 {
		t.Fatalf("compatibleProviders len = %d, want 2", len(got))
	}
	if got[0].ID != "openai" || got[1].ID != "anthropic" {
		t.Errorf("compatibleProviders = %v, want [openai, anthropic]", got)
	}
}

func TestFqnModels(t *testing.T) {
	p := config.ProviderInfo{ID: "openai", Models: []string{"gpt-5", "gpt-5-mini"}}
	got := fqnModels(p)
	want := []string{"openai/gpt-5", "openai/gpt-5-mini"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("fqnModels = %v, want %v", got, want)
	}
}

func TestStripProviderPrefix(t *testing.T) {
	cases := map[string]string{
		"openai/gpt-5":              "gpt-5",
		"anthropic/claude-sonnet-4": "claude-sonnet-4",
		"bare-model":                "bare-model",
		"provider/nested/model":     "nested/model",
	}
	for in, want := range cases {
		if got := stripProviderPrefix(in); got != want {
			t.Errorf("stripProviderPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// The headless launch (ADR 0007): flags win, LastLaunch fills what they
// leave empty, and anything left goes to the first compatible provider on
// its first backend.
func TestSpec(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	both := map[string]bool{config.EndpointOpenAIChat: true, config.EndpointOpenAIResponses: true}
	bedrock := config.ProviderInfo{ID: "bedrock", Models: []string{"claude"}, SupportedEndpoints: map[string]bool{config.EndpointBedrockConverse: true}}
	openai := config.ProviderInfo{ID: "openai", Models: []string{"gpt-5", "gpt-5-mini"}, SupportedEndpoints: both}
	anthropic := config.ProviderInfo{ID: "anthropic", Models: []string{"claude"}, SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}}
	newGlobal := func() *config.Global {
		return &config.Global{ApertureHost: testHost, Providers: []config.ProviderInfo{bedrock, openai, anthropic}}
	}
	c := &Client{}

	// Defaults: the first compatible provider, its first backend, its first model.
	spec, err := c.Spec(newGlobal(), "", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if spec.Env["COPILOT_PROVIDER_TYPE"] != "openai" || spec.Env["COPILOT_PROVIDER_WIRE_API"] != "completions" || spec.Env["COPILOT_MODEL"] != "gpt-5" {
		t.Errorf("default env = %v, want openai chat completions on gpt-5", spec.Env)
	}
	if len(spec.Args) != 0 {
		t.Errorf("default args = %v, want none", spec.Args)
	}

	// An explicit provider with no model falls back to that provider's
	// default model, same as the unflagged path.
	spec, err = c.Spec(newGlobal(), "openai", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if spec.Env["COPILOT_MODEL"] != "gpt-5" {
		t.Errorf("explicit provider env = %v, want the provider's default model", spec.Env)
	}

	// Flags win, and passthrough args come through verbatim.
	spec, err = c.Spec(newGlobal(), "anthropic", "anthropic/claude", []string{"--allow-all"})
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if spec.Env["COPILOT_PROVIDER_TYPE"] != "anthropic" || spec.Env["COPILOT_MODEL"] != "claude" {
		t.Errorf("flag env = %v, want the flagged provider and model", spec.Env)
	}
	if !slices.Equal(spec.Args, []string{"--allow-all"}) {
		t.Errorf("flag args = %v, want the passthrough", spec.Args)
	}

	// LastLaunch fills empty flags, backend included, and an explicit model still wins.
	g := newGlobal()
	g.LastLaunch = config.LaunchState{LastClientName: name, LastBackendType: "openai_responses", LastProviderID: "openai", LastModel: "openai/gpt-5-mini"}
	spec, err = c.Spec(g, "", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if spec.Env["COPILOT_PROVIDER_WIRE_API"] != "responses" || spec.Env["COPILOT_MODEL"] != "gpt-5-mini" {
		t.Errorf("replay env = %v, want the LastLaunch backend and model", spec.Env)
	}
	spec, err = c.Spec(g, "", "openai/gpt-5", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if spec.Env["COPILOT_PROVIDER_WIRE_API"] != "responses" || spec.Env["COPILOT_MODEL"] != "gpt-5" {
		t.Errorf("model flag env = %v, want the flag model on the LastLaunch backend", spec.Env)
	}

	// A stale model in LastLaunch falls back to the provider's default model.
	g = newGlobal()
	g.LastLaunch = config.LaunchState{LastClientName: name, LastBackendType: "openai_responses", LastProviderID: "openai", LastModel: "openai/stale-model"}
	spec, err = c.Spec(g, "", "", nil)
	if err != nil {
		t.Fatalf("Spec with stale model: %v", err)
	}
	if spec.Env["COPILOT_MODEL"] != "gpt-5" {
		t.Errorf("stale model env = %v, want the provider's default model", spec.Env)
	}

	// Errors: an unknown provider names the compatible ids, an incompatible
	// one names the backends it lacks.
	if _, err := c.Spec(newGlobal(), "ghost", "", nil); err == nil || !strings.Contains(err.Error(), "openai, anthropic") {
		t.Errorf("unknown provider: %v, want the compatible ids in the error", err)
	}
	if _, err := c.Spec(newGlobal(), "bedrock", "", nil); err == nil || !strings.Contains(err.Error(), "OpenAI Chat Completions") {
		t.Errorf("incompatible provider: %v, want the Copilot backends named", err)
	}
}
