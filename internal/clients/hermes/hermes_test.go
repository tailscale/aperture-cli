package hermes

import (
	"slices"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

const testHost = "http://ai.example.com"

func TestBuildEnv(t *testing.T) {
	got := buildEnv(testHost+"/", "provider/model/name")
	want := map[string]string{
		"CUSTOM_BASE_URL":        testHost + "/v1",
		"HERMES_INFERENCE_MODEL": "model/name",
	}
	if len(got) != len(want) {
		t.Fatalf("buildEnv = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("buildEnv[%q] = %q, want %q", key, got[key], value)
		}
	}
	if got := buildEnv(testHost, ""); got["HERMES_INFERENCE_MODEL"] != "" {
		t.Errorf("buildEnv with no model = %v", got)
	}
}

func TestBuildArgs(t *testing.T) {
	if got, want := buildArgs(false), []string{"--provider", "custom"}; !slices.Equal(got, want) {
		t.Errorf("buildArgs(false) = %v, want %v", got, want)
	}
	if got, want := buildArgs(true), []string{"--provider", "custom", "--yolo"}; !slices.Equal(got, want) {
		t.Errorf("buildArgs(true) = %v, want %v", got, want)
	}
}

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "match", SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}},
		{ID: "other", SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 1 || got[0].ID != "match" {
		t.Errorf("compatibleProviders = %+v", got)
	}
}

func TestModels(t *testing.T) {
	p := config.ProviderInfo{ID: "provider", Models: []string{"model/name"}}
	if got := fqnModels(p); !slices.Equal(got, []string{"provider/model/name"}) {
		t.Errorf("fqnModels = %v", got)
	}
	if got := stripProviderPrefix("provider/model/name"); got != "model/name" {
		t.Errorf("stripProviderPrefix = %q", got)
	}
}

func TestResolveReplay(t *testing.T) {
	p := config.ProviderInfo{ID: "provider", Models: []string{"model"}, SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}}
	g := &config.Global{
		Providers:  []config.ProviderInfo{p},
		LastLaunch: config.LaunchState{LastClientName: name, LastBackendType: backendType, LastProviderID: p.ID, LastModel: "provider/model"},
	}
	_, model, ok := resolveReplay(g)
	if !ok || model != "provider/model" {
		t.Fatalf("resolveReplay = %q, %v", model, ok)
	}
	g.LastLaunch.LastModel = "provider/stale"
	if _, _, ok := resolveReplay(g); ok {
		t.Error("resolveReplay accepted a stale model")
	}
	g.LastLaunch.LastModel = "provider/model"
	g.LastLaunch.LastBackendType = "openai_responses"
	if _, _, ok := resolveReplay(g); ok {
		t.Error("resolveReplay accepted a stale backend")
	}
}

func TestInstallCommandDetectsPipelineFailures(t *testing.T) {
	plan := (&Client{}).Install(&config.Global{})
	cmd, err := plan.Run()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cmd.Args, []string{
		"bash", "-o", "pipefail", "-c",
		"curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash",
	}) {
		t.Errorf("install command args = %q, want bash with pipefail", cmd.Args)
	}
}

func TestInstallUninstall(t *testing.T) {
	c := &Client{}
	if got := c.Install(&config.Global{}); got.Hint != installCmd || got.Run == nil {
		t.Errorf("Install = %+v", got)
	}
	if got := c.Uninstall(); got.Hint != uninstallCmd || got.Run == nil {
		t.Errorf("Uninstall = %+v", got)
	}
}
