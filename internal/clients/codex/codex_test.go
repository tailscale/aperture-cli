package codex

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
		{ID: "openai", Compatibility: map[string]bool{"openai_responses": true}},
		{ID: "openrouter", Compatibility: map[string]bool{"openai_chat": true}},
		{ID: "anthropic", Compatibility: map[string]bool{"anthropic_messages": true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 1 || got[0].ID != "openai" {
		t.Errorf("compatibleProviders = %+v, want [openai]", got)
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
		"openai/gpt-5":           "gpt-5",
		"vertex/gemini-2.5-pro":  "gemini-2.5-pro",
		"bare-model":             "bare-model",
		"provider/nested/model":  "nested/model",
	}
	for in, want := range cases {
		if got := stripProviderPrefix(in); got != want {
			t.Errorf("stripProviderPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	codexHome, err := writeConfig(testHost)
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	authData, err := os.ReadFile(filepath.Join(codexHome, "auth.json"))
	if err != nil {
		t.Fatalf("auth.json: %v", err)
	}
	var auth map[string]string
	if err := json.Unmarshal(authData, &auth); err != nil {
		t.Fatal(err)
	}
	if auth["auth_mode"] != "apikey" {
		t.Errorf("auth_mode = %q, want apikey", auth["auth_mode"])
	}
	if auth["OPENAI_API_KEY"] != "not-needed" {
		t.Errorf("OPENAI_API_KEY = %q, want not-needed", auth["OPENAI_API_KEY"])
	}

	tomlData, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatalf("config.toml: %v", err)
	}
	if got := string(tomlData); !containsAll(got, []string{
		"model_provider = \"aperture\"",
		"base_url = \"" + testHost + "/v1\"",
		"env_key = \"OPENAI_API_KEY\"",
	}) {
		t.Errorf("config.toml missing expected entries:\n%s", got)
	}
}

func TestInstallUninstall(t *testing.T) {
	c := &Client{}
	g := &config.Global{}

	install := c.Install(g)
	if install.Hint != "npm install -g @openai/codex" {
		t.Errorf("Install.Hint = %q", install.Hint)
	}
	if install.Run == nil {
		t.Error("Install.Run is nil")
	}

	uninstall := c.Uninstall()
	if uninstall.Hint != "npm uninstall -g @openai/codex" {
		t.Errorf("Uninstall.Hint = %q", uninstall.Hint)
	}
}

func TestReplay_StaleProvider(t *testing.T) {
	c := &Client{}
	g := &config.Global{
		LastLaunch: config.LaunchState{
			LastClientName: name,
			LastProviderID: "missing",
		},
	}
	// Binary not installed → nil regardless of provider presence.
	if cmd := c.Replay(g); cmd != nil {
		t.Error("Replay with missing binary should return nil")
	}
}

func containsAll(haystack string, needles []string) bool {
	for _, n := range needles {
		if !contains(haystack, n) {
			return false
		}
	}
	return true
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
