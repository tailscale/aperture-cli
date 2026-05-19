package copilot

import (
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
		p := config.ProviderInfo{Compatibility: map[string]bool{
			"openai_chat":      true,
			"openai_responses": true,
		}}
		bs := backendsFor(p)
		if len(bs) != 2 {
			t.Errorf("backendsFor = %d, want 2", len(bs))
		}
	})
	t.Run("anthropic_only", func(t *testing.T) {
		p := config.ProviderInfo{Compatibility: map[string]bool{"anthropic_messages": true}}
		bs := backendsFor(p)
		if len(bs) != 1 || bs[0].id != "anthropic" {
			t.Errorf("backendsFor = %+v, want [anthropic]", bs)
		}
	})
	t.Run("bedrock_none", func(t *testing.T) {
		p := config.ProviderInfo{Compatibility: map[string]bool{"bedrock": true}}
		bs := backendsFor(p)
		if len(bs) != 0 {
			t.Errorf("backendsFor = %+v, want empty", bs)
		}
	})
}

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "openai", Compatibility: map[string]bool{"openai_chat": true, "openai_responses": true}},
		{ID: "anthropic", Compatibility: map[string]bool{"anthropic_messages": true}},
		{ID: "bedrock", Compatibility: map[string]bool{"bedrock": true}},
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
