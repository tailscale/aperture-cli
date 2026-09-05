package claudecode

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/clients"
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
		"curl -fsSL https://claude.ai/install.sh | bash",
	}) {
		t.Errorf("install command args = %q, want bash with pipefail", cmd.Args)
	}
}

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

func TestEnv_Mantle(t *testing.T) {
	b := lookupBackend("mantle")
	env, err := envForBackend(testHost, b)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ANTHROPIC_BEDROCK_MANTLE_BASE_URL": testHost,
		"CLAUDE_CODE_USE_MANTLE":            "1",
		"CLAUDE_CODE_SKIP_MANTLE_AUTH":      "1",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["ANTHROPIC_MODEL"]; ok {
		t.Error("Mantle environment should let Claude Code select its own model")
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
	p := config.ProviderInfo{SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}}
	got := backendsFor(p)
	// anthropic + zai both take /v1/messages.
	if len(got) != 2 {
		t.Errorf("backendsFor = %+v", got)
	}
}

func TestDedupedBackendsFor_AnthropicVsZAI(t *testing.T) {
	p := config.ProviderInfo{SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}}
	got := dedupedBackendsFor(p)
	if len(got) != 1 || got[0].id != "anthropic" {
		t.Errorf("dedupedBackendsFor = %+v, want [anthropic]", got)
	}
}

func TestBackendsFor_Mantle(t *testing.T) {
	p := config.ProviderInfo{
		Upstream:           "bedrock-mantle",
		SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true},
	}
	got := backendsFor(p)
	if len(got) != 1 || got[0].id != "mantle" {
		t.Errorf("backendsFor = %+v, want [mantle]", got)
	}
}

func TestBackendsFor_MantleRequiresAnthropicMessages(t *testing.T) {
	p := config.ProviderInfo{Upstream: "bedrock-mantle"}
	if got := backendsFor(p); len(got) != 0 {
		t.Errorf("backendsFor = %+v, want empty", got)
	}
}

func TestBackendsFor_MantleOpenAIIsNotClaudeCompatible(t *testing.T) {
	p := config.ProviderInfo{
		ID:                 "mantle-openai",
		Upstream:           "bedrock-mantle",
		SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
	}
	if got := backendsFor(p); len(got) != 0 {
		t.Errorf("backendsFor = %+v, want empty", got)
	}
}

func TestDedupedBackendsFor_Multi(t *testing.T) {
	p := config.ProviderInfo{SupportedEndpoints: map[string]bool{
		config.EndpointAnthropicMessages: true,
		config.EndpointBedrockInvoke:     true,
	}}
	got := dedupedBackendsFor(p)
	if len(got) != 2 {
		t.Errorf("dedupedBackendsFor = %+v, want 2", got)
	}
}

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "anthropic", SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}},
		{ID: "bedrock", SupportedEndpoints: map[string]bool{config.EndpointBedrockInvoke: true}},
		{ID: "mantle", Upstream: "bedrock-mantle", SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}},
		{ID: "openai-only", SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 3 {
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
		SupportedEndpoints: map[string]bool{config.EndpointBedrockInvoke: true},
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
		Models:             []string{"claude-opus-4", "claude-sonnet-4"},
		SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true},
	}
	env := tierModelEnv(b, p)
	if len(env) != 0 {
		t.Errorf("tierModelEnv(anthropic) = %+v, want empty", env)
	}
}

func TestTierModelEnv_Mantle(t *testing.T) {
	b := lookupBackend("mantle")
	p := config.ProviderInfo{
		Upstream:           "bedrock-mantle",
		Models:             []string{"anthropic.claude-opus-5"},
		SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true},
	}
	if env := tierModelEnv(b, p); len(env) != 0 {
		t.Errorf("tierModelEnv(mantle) = %+v, want empty", env)
	}
}

func TestMantleMenuLaunchAndReplay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PATH", "")
	bin := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var specs []clients.LaunchSpec
	c := &Client{launchFn: func(spec clients.LaunchSpec) tea.Cmd {
		spec.Env = cloneMap(spec.Env)
		specs = append(specs, spec)
		return func() tea.Msg { return nil }
	}}

	p := config.ProviderInfo{
		ID:       "mantle-anthropic",
		Name:     "AWS Bedrock (Mantle) - Anthropic",
		Upstream: "bedrock-mantle",
		Models: []string{
			"anthropic.claude-opus-5",
			"anthropic.claude-sonnet-5",
		},
		// Include Bedrock Invoke support to prove that the authoritative
		// upstream type still selects only Mantle.
		SupportedEndpoints: map[string]bool{
			config.EndpointAnthropicMessages: true,
			config.EndpointBedrockInvoke:     true,
		},
	}
	g := &config.Global{
		ApertureHost: testHost,
		Providers:    []config.ProviderInfo{p},
	}
	result := c.Menu(g).Action()
	if result.Next != nil {
		t.Fatal("Mantle launch unexpectedly opened a backend or model picker")
	}
	if result.Cmd == nil || !result.PopOnDone {
		t.Fatalf("Mantle launch result = %+v, want executable command that pops on completion", result)
	}
	if len(specs) != 1 {
		t.Fatalf("launch count = %d, want 1", len(specs))
	}
	checkMantleLaunchSpec(t, specs[0], bin)
	if got := g.LastLaunch; got.LastClientName != name || got.LastBackendType != "mantle" || got.LastProviderID != p.ID || got.LastModel != "" {
		t.Errorf("recorded launch = %+v, want Claude Code/Mantle/%s with no model", got, p.ID)
	}

	if cmd := c.Replay(g); cmd == nil {
		t.Fatal("Replay returned nil for a current Mantle launch")
	}
	if len(specs) != 2 {
		t.Fatalf("launch count after Replay = %d, want 2", len(specs))
	}
	checkMantleLaunchSpec(t, specs[1], bin)
}

func checkMantleLaunchSpec(t *testing.T, spec clients.LaunchSpec, wantBinary string) {
	t.Helper()
	if spec.Binary != wantBinary {
		t.Errorf("Binary = %q, want %q", spec.Binary, wantBinary)
	}
	wantEnv := map[string]string{
		"ANTHROPIC_BEDROCK_MANTLE_BASE_URL": testHost,
		"CLAUDE_CODE_USE_MANTLE":            "1",
		"CLAUDE_CODE_SKIP_MANTLE_AUTH":      "1",
	}
	if len(spec.Env) != len(wantEnv) {
		t.Errorf("Env = %#v, want only Mantle transport variables", spec.Env)
	}
	for k, want := range wantEnv {
		if got := spec.Env[k]; got != want {
			t.Errorf("Env[%q] = %q, want %q", k, got, want)
		}
	}
	for _, key := range []string{
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	} {
		if _, ok := spec.Env[key]; ok {
			t.Errorf("Env unexpectedly contains %s", key)
		}
	}
}

func cloneMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
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
