package codex

import (
	"reflect"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

const testHost = "http://ai.example.com"

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "openai", SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true}},
		{ID: "openrouter", SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}},
		{ID: "anthropic", SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}},
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

func TestApertureLaunchConfig(t *testing.T) {
	args, env := apertureLaunchConfig(testHost)
	wantArgs := []string{
		"--config", `model_provider="tailscale_aperture_cli"`,
		"--config", `model_providers.tailscale_aperture_cli={ name = "Aperture", base_url = "http://ai.example.com/v1", env_key = "APERTURE_CODEX_API_KEY", supports_websockets = false }`,
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %#v, want %#v", args, wantArgs)
	}
	wantEnv := map[string]string{apertureAPIKeyEnv: "not-needed"}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Errorf("env = %#v, want %#v", env, wantEnv)
	}
	for _, key := range []string{"CODEX_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL"} {
		if _, ok := env[key]; ok {
			t.Errorf("env unexpectedly overrides %s", key)
		}
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
