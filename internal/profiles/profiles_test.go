package profiles_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/profiles"
)

const testHost = "http://ai.example.com"

func TestLauncher_ClaudeCode_Env_Anthropic(t *testing.T) {
	p := &profiles.ClaudeCodeProfile{}
	backends := p.SupportedBackends()
	var b profiles.Backend
	for _, bb := range backends {
		if bb.Type == profiles.BackendAnthropic {
			b = bb
			break
		}
	}
	if b.Type == "" {
		t.Fatal("BackendAnthropic not in SupportedBackends")
	}

	env, err := p.Env(testHost, b)
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
	}

	if got := env["ANTHROPIC_BASE_URL"]; got != testHost {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", got, testHost)
	}
	if got := env["ANTHROPIC_AUTH_TOKEN"]; got != "-" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want %q", got, "-")
	}
}

func TestLauncher_ClaudeCode_Env_Bedrock(t *testing.T) {
	p := &profiles.ClaudeCodeProfile{}
	b := profiles.Backend{Type: profiles.BackendBedrock, DisplayName: "AWS Bedrock"}

	env, err := p.Env(testHost, b)
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
	}

	want := map[string]string{
		"ANTHROPIC_BEDROCK_BASE_URL":    testHost + "/bedrock",
		"CLAUDE_CODE_USE_BEDROCK":       "1",
		"CLAUDE_CODE_SKIP_BEDROCK_AUTH": "1",
	}
	for k, wantV := range want {
		if got := env[k]; got != wantV {
			t.Errorf("%s = %q, want %q", k, got, wantV)
		}
	}
}

func TestLauncher_ClaudeCode_Env_Vertex(t *testing.T) {
	p := &profiles.ClaudeCodeProfile{}
	b := profiles.Backend{Type: profiles.BackendVertex, DisplayName: "Google Vertex"}

	env, err := p.Env(testHost, b)
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
	}

	want := map[string]string{
		"CLOUD_ML_REGION":             "_aperture_auto_vertex_region_",
		"CLAUDE_CODE_USE_VERTEX":      "1",
		"ANTHROPIC_VERTEX_PROJECT_ID": "_aperture_auto_vertex_project_id_",
		"ANTHROPIC_VERTEX_BASE_URL":   testHost + "/v1",
	}
	for k, wantV := range want {
		if got := env[k]; got != wantV {
			t.Errorf("%s = %q, want %q", k, got, wantV)
		}
	}
}

func TestLauncher_ClaudeCode_Env_ZAI(t *testing.T) {
	p := &profiles.ClaudeCodeProfile{}
	b := profiles.Backend{Type: profiles.BackendZAI, DisplayName: "z.ai"}

	env, err := p.Env(testHost, b)
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
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
	for k, wantV := range want {
		if got := env[k]; got != wantV {
			t.Errorf("%s = %q, want %q", k, got, wantV)
		}
	}
}

func TestLauncher_GeminiCLI_Env_Vertex(t *testing.T) {
	p := &profiles.GeminiCLIProfile{}
	b := profiles.Backend{Type: profiles.BackendVertex, DisplayName: "Google Vertex"}

	env, err := p.Env(testHost, b)
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
	}

	want := map[string]string{
		"GOOGLE_VERTEX_BASE_URL": testHost,
		"GOOGLE_API_KEY":         "not-needed",
	}
	for k, wantV := range want {
		if got := env[k]; got != wantV {
			t.Errorf("%s = %q, want %q", k, got, wantV)
		}
	}
}

func TestLauncher_StateFile_RoundTrip(t *testing.T) {
	// Use a temp dir so we don't pollute the real config.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg == "" {
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	}

	want := profiles.StateFile{
		LastProfileName: "Claude Code",
		LastBackendType: string(profiles.BackendBedrock),
	}
	if err := profiles.SaveState(want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	got, err := profiles.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if got.LastProfileName != want.LastProfileName {
		t.Errorf("LastProfileName = %q, want %q", got.LastProfileName, want.LastProfileName)
	}
	if got.LastBackendType != want.LastBackendType {
		t.Errorf("LastBackendType = %q, want %q", got.LastBackendType, want.LastBackendType)
	}
}

func TestLauncher_OpenCode_SupportedBackends_Single(t *testing.T) {
	p := &profiles.OpenCodeProfile{}
	if got := p.SupportedBackends(); len(got) != 1 {
		t.Errorf("SupportedBackends len = %d, want 1", len(got))
	}
}

func TestLauncher_OpenCode_ProviderEnv(t *testing.T) {
	p := &profiles.OpenCodeProfile{}
	b := profiles.Backend{Type: profiles.BackendOpenAI}

	bedrock := profiles.ProviderInfo{
		ID: "bedrock", Compatibility: map[string]bool{"bedrock_converse": true},
	}
	env := p.ProviderEnv(b, []profiles.ProviderInfo{bedrock})
	if env["AWS_ACCESS_KEY_ID"] != "not-needed" || env["AWS_REGION"] != "us-east-1" {
		t.Errorf("bedrock ProviderEnv = %v", env)
	}

	vertex := profiles.ProviderInfo{
		ID: "vertex", Compatibility: map[string]bool{"google_generate_content": true},
	}
	if env := p.ProviderEnv(b, []profiles.ProviderInfo{vertex}); len(env) != 0 {
		t.Errorf("vertex ProviderEnv = %v, want empty (express mode)", env)
	}

	anthropic := profiles.ProviderInfo{
		ID: "anthropic", Compatibility: map[string]bool{"anthropic_messages": true},
	}
	if env := p.ProviderEnv(b, []profiles.ProviderInfo{anthropic}); len(env) != 0 {
		t.Errorf("anthropic ProviderEnv = %v, want empty", env)
	}
}

func TestLauncher_OpenCode_WriteProviderConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	p := &profiles.OpenCodeProfile{}

	cw, ok := profiles.Profile(p).(profiles.ProviderConfigWriter)
	if !ok {
		t.Fatal("OpenCodeProfile does not implement ProviderConfigWriter")
	}

	tests := []struct {
		name        string
		provider    profiles.ProviderInfo
		wantNPM     string
		wantOptions map[string]string
	}{
		{
			name: "anthropic_messages",
			provider: profiles.ProviderInfo{
				ID: "anthropic", Name: "Anthropic",
				Models:        []string{"claude-sonnet-4-5", "claude-haiku-4-5"},
				Compatibility: map[string]bool{"anthropic_messages": true},
			},
			wantNPM: "@ai-sdk/anthropic",
			wantOptions: map[string]string{
				"baseURL": testHost + "/v1",
				"apiKey":  "not-required",
			},
		},
		{
			name: "bedrock_converse",
			provider: profiles.ProviderInfo{
				ID: "bedrock", Name: "AWS Bedrock",
				Models:        []string{"us.anthropic.claude-opus-4-7"},
				Compatibility: map[string]bool{"bedrock_converse": true},
			},
			wantNPM: "@ai-sdk/amazon-bedrock",
			wantOptions: map[string]string{
				"region":   "us-east-1",
				"endpoint": testHost + "/bedrock",
			},
		},
		{
			name: "google_generate_content",
			provider: profiles.ProviderInfo{
				ID: "vertex", Name: "Vertex",
				Models: []string{"gemini-2.5-pro"},
				Compatibility: map[string]bool{
					"google_generate_content": true,
					"google_raw_predict":      true,
				},
			},
			wantNPM: "@ai-sdk/google-vertex",
			wantOptions: map[string]string{
				"apiKey":  "not-required",
				"baseURL": testHost + "/v1/projects/_aperture_auto_vertex_project_id_/locations/_aperture_auto_vertex_region_/publishers/google",
			},
		},
		{
			name: "openai_responses",
			provider: profiles.ProviderInfo{
				ID: "openai", Name: "OpenAI",
				Models: []string{"gpt-5"},
				Compatibility: map[string]bool{
					"openai_chat":      true,
					"openai_responses": true,
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
			provider: profiles.ProviderInfo{
				ID: "openrouter", Name: "OpenRouter",
				Models:        []string{"qwen/qwen3-235b-a22b-2507"},
				Compatibility: map[string]bool{"openai_chat": true},
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
			envKey, configPath, cleanup, err := cw.WriteProviderConfig(testHost, profiles.Backend{Type: profiles.BackendOpenAI}, tt.provider)
			if err != nil {
				t.Fatalf("WriteProviderConfig returned error: %v", err)
			}
			if envKey != "OPENCODE_CONFIG" {
				t.Errorf("envKey = %q, want OPENCODE_CONFIG", envKey)
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
				t.Fatalf("config file is not valid JSON: %v", err)
			}

			prov, ok := cfg.Provider[tt.provider.ID]
			if !ok {
				t.Fatalf("provider %q not found in config", tt.provider.ID)
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
				if entry["name"] != fqn {
					t.Errorf("model %q name = %q, want %q", fqn, entry["name"], fqn)
				}
			}
			if len(prov.Whitelist) != len(tt.provider.Models) {
				t.Errorf("whitelist len = %d, want %d", len(prov.Whitelist), len(tt.provider.Models))
			}
			for i, m := range tt.provider.Models {
				fqn := tt.provider.ID + "/" + m
				if i < len(prov.Whitelist) && prov.Whitelist[i] != fqn {
					t.Errorf("whitelist[%d] = %q, want %q", i, prov.Whitelist[i], fqn)
				}
			}

			cleanup()
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Errorf("config file still exists after cleanup")
			}
		})
	}
}

func TestLauncher_Codex_Env_OpenAI(t *testing.T) {
	p := &profiles.CodexProfile{}
	env, err := p.Env(testHost, profiles.Backend{Type: profiles.BackendOpenAI})
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
	}

	want := map[string]string{
		"OPENAI_BASE_URL": testHost + "/v1",
		"OPENAI_API_KEY":  "not-needed",
	}
	for k, wantV := range want {
		if got := env[k]; got != wantV {
			t.Errorf("%s = %q, want %q", k, got, wantV)
		}
	}
}

func TestLauncher_Codex_Env_UnsupportedBackend(t *testing.T) {
	p := &profiles.CodexProfile{}
	_, err := p.Env(testHost, profiles.Backend{Type: profiles.BackendAnthropic})
	if err == nil {
		t.Fatal("expected error for unsupported backend, got nil")
	}
}

func TestLauncher_Codex_YoloArgs(t *testing.T) {
	p := &profiles.CodexProfile{}
	args := p.YoloArgs()
	if len(args) != 1 || args[0] != "--dangerously-bypass-approvals-and-sandbox" {
		t.Errorf("YoloArgs() = %v, want [--dangerously-bypass-approvals-and-sandbox]", args)
	}
}

func TestLauncher_Codex_ModelArgs(t *testing.T) {
	p := &profiles.CodexProfile{}
	args := p.ModelArgs("test-provider/gpt-5.3-codex")
	want := []string{"--model", "test-provider/gpt-5.3-codex"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("ModelArgs() = %v, want %v", args, want)
	}
}

func TestLauncher_Codex_WriteConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	p := &profiles.CodexProfile{}

	cw, ok := profiles.Profile(p).(profiles.ConfigWriter)
	if !ok {
		t.Fatal("CodexProfile does not implement ConfigWriter")
	}

	envKey, configPath, cleanup, err := cw.WriteConfig(testHost, profiles.Backend{Type: profiles.BackendOpenAI})
	if err != nil {
		t.Fatalf("WriteConfig returned error: %v", err)
	}
	defer cleanup()

	if envKey != "CODEX_HOME" {
		t.Errorf("envKey = %q, want %q", envKey, "CODEX_HOME")
	}

	authPath := filepath.Join(configPath, "auth.json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("auth.json not readable: %v", err)
	}

	var auth map[string]string
	if err := json.Unmarshal(data, &auth); err != nil {
		t.Fatalf("auth.json is not valid JSON: %v", err)
	}
	if got := auth["auth_mode"]; got != "apikey" {
		t.Errorf("auth_mode = %q, want %q", got, "apikey")
	}
	if got := auth["OPENAI_API_KEY"]; got != "not-needed" {
		t.Errorf("OPENAI_API_KEY = %q, want %q", got, "not-needed")
	}
}

func TestLauncher_Codex_InstallHint(t *testing.T) {
	p := &profiles.CodexProfile{}
	want := "npm install -g @openai/codex"
	if got := p.InstallHint(); got != want {
		t.Errorf("InstallHint() = %q, want %q", got, want)
	}
}

func TestLauncher_ValidCombos_NoInstalledAgents(t *testing.T) {
	// Put PATH to an empty dir so no binaries are found.
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	t.Setenv("HOME", tmp)

	mgr := profiles.NewManager()
	combos := mgr.ValidCombos(nil)

	// All built-in profiles require a real binary, so with an empty PATH
	// and a HOME with no binaries there should be zero combos.
	if len(combos) != 0 {
		t.Errorf("expected zero combos with empty PATH, got %d", len(combos))
	}
}

func TestLauncher_FilteredBackends_MatchingProvider(t *testing.T) {
	mgr := profiles.NewManager()
	p := &profiles.ClaudeCodeProfile{}
	providers := []profiles.ProviderInfo{
		{
			ID:   "test-provider",
			Name: "Test",
			Compatibility: map[string]bool{
				"anthropic_messages": true,
			},
		},
	}

	backends := mgr.FilteredBackends(p, providers)
	if len(backends) == 0 {
		t.Fatal("expected at least one backend, got none")
	}

	found := false
	for _, b := range backends {
		if b.Type == profiles.BackendAnthropic {
			found = true
		}
	}
	if !found {
		t.Error("expected Anthropic backend to be kept, but it was filtered out")
	}
}

func TestLauncher_FilteredBackends_NoMatchingProvider(t *testing.T) {
	mgr := profiles.NewManager()
	p := &profiles.ClaudeCodeProfile{}
	providers := []profiles.ProviderInfo{
		{
			ID:   "openai-only",
			Name: "OpenAI Only",
			Compatibility: map[string]bool{
				"openai_chat": true,
			},
		},
	}

	backends := mgr.FilteredBackends(p, providers)
	if len(backends) != 0 {
		t.Errorf("expected zero backends for ClaudeCode with only openai_chat provider, got %d", len(backends))
	}
}

func TestLauncher_FilteredBackends_NilProviders(t *testing.T) {
	mgr := profiles.NewManager()
	p := &profiles.ClaudeCodeProfile{}

	backends := mgr.FilteredBackends(p, nil)
	if len(backends) != len(p.SupportedBackends()) {
		t.Errorf("nil providers should return all backends; got %d, want %d",
			len(backends), len(p.SupportedBackends()))
	}
}

func TestLauncher_RequiredCompat_OpenCode(t *testing.T) {
	p := &profiles.OpenCodeProfile{}
	keys := p.RequiredCompat(profiles.Backend{})
	if len(keys) == 0 {
		t.Fatal("expected at least one compat key for OpenCode")
	}

	// Verify that providers with any of the supported protocols appear as
	// compatible for OpenCode.
	mgr := profiles.NewManager()
	for _, compat := range []string{"anthropic_messages", "bedrock_converse", "google_generate_content", "openai_chat"} {
		providers := []profiles.ProviderInfo{
			{ID: "p", Compatibility: map[string]bool{compat: true}},
		}
		if got := mgr.CompatibleProviders(p, providers); len(got) != 1 {
			t.Errorf("provider with %q not compatible with OpenCode", compat)
		}
	}
}

func TestLauncher_FindBinary_FallbackToCommonPaths(t *testing.T) {
	// Set PATH to an empty dir so exec.LookPath won't find anything.
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	t.Setenv("HOME", tmp)

	// Create a fake binary at the OpenCode-specific common path.
	binDir := filepath.Join(tmp, ".opencode", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBinary := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(fakeBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := &profiles.OpenCodeProfile{}

	// FindBinary should discover it via CommonPaths even though PATH is empty.
	got := profiles.FindBinary(p)
	if got != fakeBinary {
		t.Errorf("FindBinary() = %q, want %q", got, fakeBinary)
	}

	// IsInstalled should also return true.
	if !profiles.IsInstalled(p) {
		t.Error("IsInstalled() = false, want true")
	}
}

func TestLauncher_FindBinary_FallbackToGeneralBinDirs(t *testing.T) {
	// Set PATH to an empty dir so exec.LookPath won't find anything.
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	t.Setenv("HOME", tmp)

	// Create a fake binary in ~/.local/bin (a general common bin dir).
	localBin := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeBinary := filepath.Join(localBin, "claude")
	if err := os.WriteFile(fakeBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := &profiles.ClaudeCodeProfile{}

	// ClaudeCodeProfile.CommonPaths includes ~/.local/bin/claude, so it
	// should be found via the profile-specific path.
	got := profiles.FindBinary(p)
	if got != fakeBinary {
		t.Errorf("FindBinary() = %q, want %q", got, fakeBinary)
	}
}

func TestLauncher_FindBinary_NotFound(t *testing.T) {
	// Set PATH and HOME to an empty temp dir.
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	t.Setenv("HOME", tmp)

	p := &profiles.ClaudeCodeProfile{}
	got := profiles.FindBinary(p)
	if got != "" {
		t.Errorf("FindBinary() = %q, want empty string", got)
	}
	if profiles.IsInstalled(p) {
		t.Error("IsInstalled() = true, want false")
	}
}

func TestLauncher_FindBinary_PrefersPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Create a fake binary on PATH.
	pathBin := filepath.Join(tmp, "pathbin")
	if err := os.MkdirAll(pathBin, 0o755); err != nil {
		t.Fatal(err)
	}
	pathBinary := filepath.Join(pathBin, "opencode")
	if err := os.WriteFile(pathBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Also create a binary at the common path.
	commonBin := filepath.Join(tmp, ".opencode", "bin")
	if err := os.MkdirAll(commonBin, 0o755); err != nil {
		t.Fatal(err)
	}
	commonBinary := filepath.Join(commonBin, "opencode")
	if err := os.WriteFile(commonBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", pathBin)

	p := &profiles.OpenCodeProfile{}
	got := profiles.FindBinary(p)
	// Should prefer the PATH binary over the common path.
	if got != pathBinary {
		t.Errorf("FindBinary() = %q, want %q (PATH should be preferred)", got, pathBinary)
	}
}

func TestLauncher_FindBinary_SkipsNonExecutable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	t.Setenv("HOME", tmp)

	// Create a file at the common path but without execute permission.
	binDir := filepath.Join(tmp, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nonExec := filepath.Join(binDir, "claude")
	if err := os.WriteFile(nonExec, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &profiles.ClaudeCodeProfile{}
	got := profiles.FindBinary(p)
	if got != "" {
		t.Errorf("FindBinary() = %q, want empty string (file is not executable)", got)
	}
}

func TestLauncher_ClaudeCode_Check_NoSettingsFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	p := &profiles.ClaudeCodeProfile{}
	b := profiles.Backend{Type: profiles.BackendAnthropic}
	if err := p.Check(b); err != nil {
		t.Fatalf("Check returned error when settings.json missing: %v", err)
	}
}

func TestLauncher_ClaudeCode_Check_NoConflicts(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"env":{"SOME_UNRELATED_VAR":"hello"}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &profiles.ClaudeCodeProfile{}
	b := profiles.Backend{Type: profiles.BackendAnthropic}
	if err := p.Check(b); err != nil {
		t.Fatalf("Check returned error with no conflicting vars: %v", err)
	}
}

func TestLauncher_ClaudeCode_Check_WithConflicts(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"env":{"ANTHROPIC_BASE_URL":"https://example.com","CLAUDE_CODE_USE_BEDROCK":"1"}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &profiles.ClaudeCodeProfile{}
	b := profiles.Backend{Type: profiles.BackendAnthropic}
	err := p.Check(b)
	if err == nil {
		t.Fatal("Check returned nil, expected error for conflicting vars")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "ANTHROPIC_BASE_URL") {
		t.Errorf("error should mention ANTHROPIC_BASE_URL, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "CLAUDE_CODE_USE_BEDROCK") {
		t.Errorf("error should mention CLAUDE_CODE_USE_BEDROCK, got: %s", errMsg)
	}
}

func TestLauncher_ClaudeCode_Check_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("{not json}"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &profiles.ClaudeCodeProfile{}
	b := profiles.Backend{Type: profiles.BackendAnthropic}
	err := p.Check(b)
	if err == nil {
		t.Fatal("Check returned nil, expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention invalid JSON, got: %s", err.Error())
	}
}

func TestLauncher_ClaudeCode_Check_EmptyEnv(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"env":{}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &profiles.ClaudeCodeProfile{}
	b := profiles.Backend{Type: profiles.BackendAnthropic}
	if err := p.Check(b); err != nil {
		t.Fatalf("Check returned error with empty env: %v", err)
	}
}

func TestLauncher_ClaudeDesktop_GatewayURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://ai", "https://ai"},
		{"https://my-aperture.ts.net", "https://my-aperture.ts.net"},
		{"http://ai/", "https://ai"},
		{"https://aperture.example.com/", "https://aperture.example.com"},
		{"ai.example.com", "https://ai.example.com"},
		{"http://ai:8080/", "https://ai:8080"},
	}
	for _, tt := range tests {
		got := profiles.GatewayURL(tt.input)
		if got != tt.want {
			t.Errorf("GatewayURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLauncher_ClaudeDesktop_ImplementsLauncher(t *testing.T) {
	p := &profiles.ClaudeDesktopProfile{}
	if _, ok := profiles.Profile(p).(profiles.Launcher); !ok {
		t.Fatal("ClaudeDesktopProfile does not implement Launcher")
	}
}

func TestLauncher_ClaudeDesktop_ImplementsHostAwareInstaller(t *testing.T) {
	p := &profiles.ClaudeDesktopProfile{}
	if _, ok := profiles.Profile(p).(profiles.HostAwareInstaller); !ok {
		t.Fatal("ClaudeDesktopProfile does not implement HostAwareInstaller")
	}
}

func TestLauncher_ClaudeDesktop_SupportedBackends(t *testing.T) {
	p := &profiles.ClaudeDesktopProfile{}
	backends := p.SupportedBackends()
	if len(backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(backends))
	}
	if backends[0].Type != profiles.BackendAnthropic {
		t.Errorf("backend type = %q, want %q", backends[0].Type, profiles.BackendAnthropic)
	}
}

func TestLauncher_CompatibleProviders(t *testing.T) {
	mgr := profiles.NewManager()
	p := &profiles.ClaudeCodeProfile{}
	providers := []profiles.ProviderInfo{
		{
			ID:   "anthropic",
			Name: "Anthropic",
			Compatibility: map[string]bool{
				"anthropic_messages": true,
			},
		},
		{
			ID:   "bedrock",
			Name: "AWS Bedrock",
			Compatibility: map[string]bool{
				"bedrock_model_invoke": true,
			},
		},
		{
			ID:   "openai-only",
			Name: "OpenAI Only",
			Compatibility: map[string]bool{
				"openai_chat": true,
			},
		},
	}

	compatible := mgr.CompatibleProviders(p, providers)
	if len(compatible) != 2 {
		t.Fatalf("expected 2 compatible providers, got %d", len(compatible))
	}

	gotIDs := make(map[string]bool)
	for _, prov := range compatible {
		gotIDs[prov.ID] = true
	}
	if !gotIDs["anthropic"] || !gotIDs["bedrock"] {
		t.Errorf("expected anthropic and bedrock, got IDs: %v", compatible)
	}
}

func TestLauncher_CompatibleProviders_NoCompatChecker(t *testing.T) {
	// Create a profile that does not implement CompatChecker.
	mgr := profiles.NewManager()
	p := &noCompatProfile{}
	providers := []profiles.ProviderInfo{
		{ID: "a", Name: "A", Compatibility: map[string]bool{"x": true}},
		{ID: "b", Name: "B", Compatibility: map[string]bool{"y": true}},
	}

	compatible := mgr.CompatibleProviders(p, providers)
	if len(compatible) != 2 {
		t.Errorf("expected all providers returned for non-CompatChecker profile, got %d", len(compatible))
	}
}

func TestLauncher_CompatibleProviders_NoMatch(t *testing.T) {
	mgr := profiles.NewManager()
	p := &profiles.ClaudeCodeProfile{}
	providers := []profiles.ProviderInfo{
		{
			ID:   "openai-only",
			Name: "OpenAI Only",
			Compatibility: map[string]bool{
				"openai_chat": true,
			},
		},
	}

	compatible := mgr.CompatibleProviders(p, providers)
	if len(compatible) != 0 {
		t.Errorf("expected 0 compatible providers, got %d", len(compatible))
	}
}

func TestLauncher_BackendsForProvider(t *testing.T) {
	mgr := profiles.NewManager()
	p := &profiles.ClaudeCodeProfile{}
	provider := profiles.ProviderInfo{
		ID:   "multi",
		Name: "Multi",
		Compatibility: map[string]bool{
			"anthropic_messages":   true,
			"bedrock_model_invoke": true,
		},
	}

	backends := mgr.BackendsForProvider(p, provider)
	// anthropic_messages matches both Anthropic and ZAI backends;
	// bedrock_model_invoke matches Bedrock.
	if len(backends) != 3 {
		t.Fatalf("expected 3 backends, got %d", len(backends))
	}

	gotTypes := make(map[profiles.BackendType]bool)
	for _, b := range backends {
		gotTypes[b.Type] = true
	}
	if !gotTypes[profiles.BackendAnthropic] || !gotTypes[profiles.BackendBedrock] || !gotTypes[profiles.BackendZAI] {
		t.Errorf("expected anthropic, zai, and bedrock, got: %v", backends)
	}
}

func TestLauncher_BackendsForProvider_SingleBackend(t *testing.T) {
	mgr := profiles.NewManager()
	p := &profiles.ClaudeCodeProfile{}
	provider := profiles.ProviderInfo{
		ID:   "bedrock-only",
		Name: "Bedrock Only",
		Compatibility: map[string]bool{
			"bedrock_model_invoke": true,
		},
	}

	backends := mgr.BackendsForProvider(p, provider)
	if len(backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(backends))
	}
	if backends[0].Type != profiles.BackendBedrock {
		t.Errorf("backend type = %q, want %q", backends[0].Type, profiles.BackendBedrock)
	}
}

func TestLauncher_BackendsForProvider_NoCompatChecker(t *testing.T) {
	mgr := profiles.NewManager()
	p := &noCompatProfile{}
	provider := profiles.ProviderInfo{
		ID:            "any",
		Name:          "Any",
		Compatibility: map[string]bool{},
	}

	backends := mgr.BackendsForProvider(p, provider)
	if len(backends) != len(p.SupportedBackends()) {
		t.Errorf("expected all backends for non-CompatChecker profile, got %d want %d",
			len(backends), len(p.SupportedBackends()))
	}
}

func TestLauncher_DedupBackends(t *testing.T) {
	mgr := profiles.NewManager()
	p := &profiles.ClaudeCodeProfile{}
	provider := profiles.ProviderInfo{
		ID:   "multi",
		Name: "Multi",
		Compatibility: map[string]bool{
			"anthropic_messages":   true,
			"bedrock_model_invoke": true,
		},
	}

	backends := mgr.BackendsForProvider(p, provider)
	// Before dedup: Anthropic + ZAI (both anthropic_messages) + Bedrock = 3
	if len(backends) != 3 {
		t.Fatalf("expected 3 backends before dedup, got %d", len(backends))
	}

	deduped := mgr.DedupBackends(p, backends)
	// After dedup: Anthropic (first of the anthropic_messages group) + Bedrock = 2
	if len(deduped) != 2 {
		t.Fatalf("expected 2 backends after dedup, got %d", len(deduped))
	}

	gotTypes := make(map[profiles.BackendType]bool)
	for _, b := range deduped {
		gotTypes[b.Type] = true
	}
	if !gotTypes[profiles.BackendAnthropic] || !gotTypes[profiles.BackendBedrock] {
		t.Errorf("expected Anthropic and Bedrock after dedup, got: %v", deduped)
	}
	// ZAI should have been deduped away (same compat key as Anthropic).
	if gotTypes[profiles.BackendZAI] {
		t.Error("ZAI should have been deduplicated since it shares anthropic_messages with Anthropic")
	}
}

func TestLauncher_DedupBackends_NoCompatChecker(t *testing.T) {
	mgr := profiles.NewManager()
	p := &noCompatProfile{}
	backends := p.SupportedBackends()

	deduped := mgr.DedupBackends(p, backends)
	if len(deduped) != len(backends) {
		t.Errorf("expected no dedup for non-CompatChecker profile, got %d want %d",
			len(deduped), len(backends))
	}
}

func TestLauncher_ClaudeCode_ApplyModel(t *testing.T) {
	p := &profiles.ClaudeCodeProfile{}
	env := map[string]string{"ANTHROPIC_BASE_URL": "http://ai"}
	p.ApplyModel("claude-sonnet-4-20250514", env)
	if env["ANTHROPIC_MODEL"] != "claude-sonnet-4-20250514" {
		t.Errorf("ANTHROPIC_MODEL = %q, want %q", env["ANTHROPIC_MODEL"], "claude-sonnet-4-20250514")
	}
}

func TestLauncher_StateFile_LastProviderID_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg == "" {
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	}

	want := profiles.StateFile{
		LastProfileName: "Claude Code",
		LastBackendType: string(profiles.BackendAnthropic),
		LastProviderID:  "anthropic-via-aperture",
		LastModel:       "anthropic-via-aperture/claude-sonnet",
	}
	if err := profiles.SaveState(want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	got, err := profiles.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if got.LastProviderID != want.LastProviderID {
		t.Errorf("LastProviderID = %q, want %q", got.LastProviderID, want.LastProviderID)
	}
	if got.LastModel != want.LastModel {
		t.Errorf("LastModel = %q, want %q", got.LastModel, want.LastModel)
	}
}

// noCompatProfile is a test Profile that does not implement CompatChecker.
type noCompatProfile struct{}

func (noCompatProfile) Name() string       { return "no-compat" }
func (noCompatProfile) BinaryName() string { return "no-compat-binary" }
func (noCompatProfile) SupportedBackends() []profiles.Backend {
	return []profiles.Backend{
		{Type: profiles.BackendAnthropic, DisplayName: "Anthropic"},
		{Type: profiles.BackendOpenAI, DisplayName: "OpenAI"},
	}
}
func (noCompatProfile) Env(string, profiles.Backend) (map[string]string, error) {
	return nil, nil
}

func TestLauncher_AllProfiles_ImplementPathHinter(t *testing.T) {
	mgr := profiles.NewManager()
	for _, p := range mgr.AllProfiles() {
		if _, ok := p.(profiles.PathHinter); !ok {
			t.Errorf("profile %q does not implement PathHinter", p.Name())
		}
	}
}
