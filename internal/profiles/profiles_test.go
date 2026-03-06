package profiles_test

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	p.SetVertexProjectID("my-test-project")
	b := profiles.Backend{Type: profiles.BackendVertex, DisplayName: "Google Vertex"}

	env, err := p.Env(testHost, b)
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
	}

	want := map[string]string{
		"CLOUD_ML_REGION":             "global",
		"CLAUDE_CODE_USE_VERTEX":      "1",
		"ANTHROPIC_VERTEX_PROJECT_ID": "my-test-project",
		"ANTHROPIC_VERTEX_BASE_URL":   testHost + "/v1",
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

func TestLauncher_OpenCode_Env_Anthropic(t *testing.T) {
	p := &profiles.OpenCodeProfile{}
	env, err := p.Env(testHost, profiles.Backend{Type: profiles.BackendAnthropic})
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != testHost+"/bedrock" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", got, testHost+"/bedrock")
	}
}

func TestLauncher_OpenCode_Env_Bedrock(t *testing.T) {
	p := &profiles.OpenCodeProfile{}
	env, err := p.Env(testHost, profiles.Backend{Type: profiles.BackendBedrock})
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
	}
	if got := env["AWS_ACCESS_KEY_ID"]; got != "not-needed" {
		t.Errorf("AWS_ACCESS_KEY_ID = %q, want %q", got, "not-needed")
	}
	if got := env["AWS_REGION"]; got != "us-east-1" {
		t.Errorf("AWS_REGION = %q, want %q", got, "us-east-1")
	}
}

func TestLauncher_OpenCode_Env_Vertex(t *testing.T) {
	p := &profiles.OpenCodeProfile{}
	p.SetVertexProjectID("my-test-project")
	env, err := p.Env(testHost, profiles.Backend{Type: profiles.BackendVertex})
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
	}
	if got := env["GOOGLE_CLOUD_PROJECT"]; got != "my-test-project" {
		t.Errorf("GOOGLE_CLOUD_PROJECT = %q, want %q", got, "my-test-project")
	}
	if got := env["GOOGLE_CLOUD_LOCATION"]; got != "global" {
		t.Errorf("GOOGLE_CLOUD_LOCATION = %q, want %q", got, "global")
	}
}

func TestLauncher_OpenCode_Env_OpenAI(t *testing.T) {
	p := &profiles.OpenCodeProfile{}
	env, err := p.Env(testHost, profiles.Backend{Type: profiles.BackendOpenAI})
	if err != nil {
		t.Fatalf("Env returned error: %v", err)
	}
	if got := env["OPENAI_BASE_URL"]; got != testHost+"/v1" {
		t.Errorf("OPENAI_BASE_URL = %q, want %q", got, testHost+"/v1")
	}
}

func TestLauncher_OpenCode_WriteConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	p := &profiles.OpenCodeProfile{}
	p.SetVertexProjectID("my-test-project")

	cw, ok := profiles.Profile(p).(profiles.ConfigWriter)
	if !ok {
		t.Fatal("OpenCodeProfile does not implement ConfigWriter")
	}

	tests := []struct {
		name        string
		backend     profiles.Backend
		wantKey     string
		wantOptions map[string]string
	}{
		{
			name:    "anthropic",
			backend: profiles.Backend{Type: profiles.BackendAnthropic},
			wantKey: "anthropic",
			wantOptions: map[string]string{
				"apiKey":  "{env:ANTHROPIC_AUTH_TOKEN}",
				"baseURL": "{env:ANTHROPIC_BASE_URL}",
			},
		},
		{
			name:    "bedrock",
			backend: profiles.Backend{Type: profiles.BackendBedrock},
			wantKey: "amazon-bedrock",
			wantOptions: map[string]string{
				"region":   "us-east-1",
				"endpoint": testHost + "/bedrock",
			},
		},
		{
			name:    "vertex",
			backend: profiles.Backend{Type: profiles.BackendVertex},
			wantKey: "google-vertex",
			wantOptions: map[string]string{
				"project":  "my-test-project",
				"location": "global",
				"baseURL":  testHost + "/v1",
			},
		},
		{
			name:    "openai",
			backend: profiles.Backend{Type: profiles.BackendOpenAI},
			wantKey: "openai",
			wantOptions: map[string]string{
				"apiKey":  "{env:OPENAI_API_KEY}",
				"baseURL": "{env:OPENAI_BASE_URL}",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, configPath, cleanup, err := cw.WriteConfig(testHost, tt.backend)
			if err != nil {
				t.Fatalf("WriteConfig returned error: %v", err)
			}

			// File must exist
			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("config file not readable: %v", err)
			}

			// Must be valid JSON with expected structure
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("config file is not valid JSON: %v", err)
			}
			providerRaw, ok := raw["provider"]
			if !ok {
				t.Fatal("config missing 'provider' key")
			}
			var providers map[string]struct {
				Options map[string]string `json:"options"`
			}
			if err := json.Unmarshal(providerRaw, &providers); err != nil {
				t.Fatalf("provider not valid JSON: %v", err)
			}
			prov, ok := providers[tt.wantKey]
			if !ok {
				t.Fatalf("provider %q not found in config", tt.wantKey)
			}
			for k, want := range tt.wantOptions {
				if got := prov.Options[k]; got != want {
					t.Errorf("options[%q] = %q, want %q", k, got, want)
				}
			}

			// cleanup removes the file
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
	if len(args) != 1 || args[0] != "--full-auto" {
		t.Errorf("YoloArgs() = %v, want [--full-auto]", args)
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

func TestLauncher_RequiredCompat_OpenCodeBedrock(t *testing.T) {
	p := &profiles.OpenCodeProfile{}
	keys := p.RequiredCompat(profiles.Backend{Type: profiles.BackendBedrock})
	if len(keys) == 0 {
		t.Fatal("expected at least one compat key for OpenCode+Bedrock")
	}

	// Verify that a provider with bedrock_converse satisfies the requirement.
	mgr := profiles.NewManager()
	providers := []profiles.ProviderInfo{
		{
			ID:   "bedrock-provider",
			Name: "Bedrock",
			Compatibility: map[string]bool{
				"bedrock_converse": true,
			},
		},
	}
	backends := mgr.FilteredBackends(p, providers)
	found := false
	for _, b := range backends {
		if b.Type == profiles.BackendBedrock {
			found = true
		}
	}
	if !found {
		t.Error("expected Bedrock backend to be available with bedrock_converse provider")
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

func TestLauncher_AllProfiles_ImplementPathHinter(t *testing.T) {
	mgr := profiles.NewManager()
	for _, p := range mgr.AllProfiles() {
		if _, ok := p.(profiles.PathHinter); !ok {
			t.Errorf("profile %q does not implement PathHinter", p.Name())
		}
	}
}
