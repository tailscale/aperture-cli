package claudecode

import (
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

const testHost = "http://ai.example.com"

func TestEnv_Anthropic(t *testing.T) {
	env, err := envForBackend(testHost, backends[0])
	if err != nil {
		t.Fatal(err)
	}
	if env["ANTHROPIC_BASE_URL"] != testHost {
		t.Errorf("ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != "-" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q", env["ANTHROPIC_AUTH_TOKEN"])
	}
}

func TestEnv_Bedrock(t *testing.T) {
	b := lookupBackend("bedrock")
	env, err := envForBackend(testHost, b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ANTHROPIC_BEDROCK_BASE_URL":    testHost + "/bedrock",
		"CLAUDE_CODE_USE_BEDROCK":       "1",
		"CLAUDE_CODE_SKIP_BEDROCK_AUTH": "1",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func TestEnv_Vertex(t *testing.T) {
	b := lookupBackend("vertex")
	env, err := envForBackend(testHost, b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"CLOUD_ML_REGION":             "_aperture_auto_vertex_region_",
		"CLAUDE_CODE_USE_VERTEX":      "1",
		"ANTHROPIC_VERTEX_PROJECT_ID": "_aperture_auto_vertex_project_id_",
		"ANTHROPIC_VERTEX_BASE_URL":   testHost + "/v1",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func TestEnv_ZAI(t *testing.T) {
	b := lookupBackend("zai")
	env, err := envForBackend(testHost, b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ANTHROPIC_BASE_URL":             testHost,
		"ANTHROPIC_MODEL":                "glm-5.1",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "glm-5.1",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "glm-5.1",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "glm-5-turbo",
		"API_TIMEOUT_MS":                 "3000000",
		"ANTHROPIC_API_KEY":              "-",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func TestApplyModel_StripsProviderPrefix(t *testing.T) {
	env := map[string]string{}
	applyModel("bedrock/anthropic.claude-opus-4-7", env)
	if env["ANTHROPIC_MODEL"] != "anthropic.claude-opus-4-7" {
		t.Errorf("ANTHROPIC_MODEL = %q, want %q", env["ANTHROPIC_MODEL"], "anthropic.claude-opus-4-7")
	}
}

func TestApplyModel_Bare(t *testing.T) {
	env := map[string]string{}
	applyModel("claude-sonnet-4-20250514", env)
	if env["ANTHROPIC_MODEL"] != "claude-sonnet-4-20250514" {
		t.Errorf("ANTHROPIC_MODEL = %q", env["ANTHROPIC_MODEL"])
	}
}

func TestBackendsFor_Anthropic(t *testing.T) {
	p := config.ProviderInfo{Compatibility: map[string]bool{"anthropic_messages": true}}
	got := backendsFor(p)
	// anthropic + zai both take anthropic_messages.
	if len(got) != 2 {
		t.Errorf("backendsFor = %+v", got)
	}
}

func TestDedupedBackendsFor_AnthropicVsZAI(t *testing.T) {
	p := config.ProviderInfo{Compatibility: map[string]bool{"anthropic_messages": true}}
	got := dedupedBackendsFor(p)
	if len(got) != 1 || got[0].id != "anthropic" {
		t.Errorf("dedupedBackendsFor = %+v, want [anthropic]", got)
	}
}

func TestDedupedBackendsFor_Multi(t *testing.T) {
	p := config.ProviderInfo{Compatibility: map[string]bool{
		"anthropic_messages":   true,
		"bedrock_model_invoke": true,
	}}
	got := dedupedBackendsFor(p)
	if len(got) != 2 {
		t.Errorf("dedupedBackendsFor = %+v, want 2", got)
	}
}

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "anthropic", Compatibility: map[string]bool{"anthropic_messages": true}},
		{ID: "bedrock", Compatibility: map[string]bool{"bedrock_model_invoke": true}},
		{ID: "openai-only", Compatibility: map[string]bool{"openai_chat": true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 2 {
		t.Errorf("compatibleProviders = %+v", got)
	}
}

func TestTierModelEnv_Bedrock(t *testing.T) {
	b := lookupBackend("bedrock")
	p := config.ProviderInfo{
		Models: []string{
			"us.anthropic.claude-opus-4-1-20250805-v1:0",
			"us.anthropic.claude-sonnet-4-5-20250929-v1:0",
			"us.anthropic.claude-haiku-4-5-20251001-v1:0",
		},
		Compatibility: map[string]bool{"bedrock_model_invoke": true},
	}
	env := tierModelEnv(b, p)
	if !containsSubstr(env["ANTHROPIC_DEFAULT_OPUS_MODEL"], "opus") {
		t.Errorf("OPUS tier = %q", env["ANTHROPIC_DEFAULT_OPUS_MODEL"])
	}
	if !containsSubstr(env["ANTHROPIC_DEFAULT_SONNET_MODEL"], "sonnet") {
		t.Errorf("SONNET tier = %q", env["ANTHROPIC_DEFAULT_SONNET_MODEL"])
	}
	if !containsSubstr(env["ANTHROPIC_DEFAULT_HAIKU_MODEL"], "haiku") {
		t.Errorf("HAIKU tier = %q", env["ANTHROPIC_DEFAULT_HAIKU_MODEL"])
	}
}

func TestTierModelEnv_NonBedrock(t *testing.T) {
	b := lookupBackend("anthropic")
	p := config.ProviderInfo{
		Models:        []string{"claude-opus-4", "claude-sonnet-4"},
		Compatibility: map[string]bool{"anthropic_messages": true},
	}
	env := tierModelEnv(b, p)
	if len(env) != 0 {
		t.Errorf("tierModelEnv(anthropic) = %+v, want empty", env)
	}
}

func lookupBackend(id string) backend {
	for _, b := range backends {
		if b.id == id {
			return b
		}
	}
	panic("unknown backend id: " + id)
}

func containsSubstr(s, sub string) bool {
	if s == "" {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if lower(s[i:i+len(sub)]) == sub {
			return true
		}
	}
	return false
}

func lower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
