package hermes

import (
	"slices"
	"strings"
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

func TestSpec(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", tmp)
	chat := map[string]bool{config.EndpointOpenAIChat: true}
	openrouter := config.ProviderInfo{ID: "openrouter", Models: []string{"gpt-5", "gpt-5-mini"}, SupportedEndpoints: chat}
	responses := config.ProviderInfo{ID: "openai", Models: []string{"gpt-5"}, SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true}}
	anthropic := config.ProviderInfo{ID: "anthropic", Models: []string{"claude"}, SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}}
	newGlobal := func() *config.Global {
		return &config.Global{ApertureHost: testHost, Providers: []config.ProviderInfo{anthropic, responses, openrouter}}
	}
	c := &Client{}
	baseArgs := []string{"--provider", "custom"}

	// Defaults: the first compatible provider and its first model.
	spec, err := c.Spec(newGlobal(), "", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if got := spec.Env["HERMES_INFERENCE_MODEL"]; got != "gpt-5" {
		t.Errorf("default model = %q, want the first compatible provider's first model", got)
	}
	if !slices.Equal(spec.Args, baseArgs) {
		t.Errorf("default args = %v, want %v", spec.Args, baseArgs)
	}

	// Flags win, and passthrough args land after aperture's own.
	g := newGlobal()
	g.Settings.YoloMode = true
	spec, err = c.Spec(g, "openrouter", "openrouter/gpt-5-mini", []string{"--resume", "abc"})
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if got := spec.Env["HERMES_INFERENCE_MODEL"]; got != "gpt-5-mini" {
		t.Errorf("flag model = %q, want gpt-5-mini", got)
	}
	if want := []string{"--provider", "custom", "--yolo", "--resume", "abc"}; !slices.Equal(spec.Args, want) {
		t.Errorf("flag args = %v, want %v", spec.Args, want)
	}

	// An explicit provider with no model takes that provider's first model.
	spec, err = c.Spec(newGlobal(), "openrouter", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if got := spec.Env["HERMES_INFERENCE_MODEL"]; got != "gpt-5" {
		t.Errorf("provider default model = %q, want the provider's first model", got)
	}

	// LastLaunch fills empty flags when it still fits.
	g = newGlobal()
	g.LastLaunch = config.LaunchState{LastClientName: name, LastBackendType: backendType, LastProviderID: "openrouter", LastModel: "openrouter/gpt-5-mini"}
	spec, err = c.Spec(g, "", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if got := spec.Env["HERMES_INFERENCE_MODEL"]; got != "gpt-5-mini" {
		t.Errorf("replay model = %q, want LastLaunch's model", got)
	}

	// A stale model in LastLaunch falls back to the provider's default model.
	g = newGlobal()
	g.LastLaunch = config.LaunchState{LastClientName: name, LastBackendType: backendType, LastProviderID: "openrouter", LastModel: "openrouter/stale-model"}
	spec, err = c.Spec(g, "", "", nil)
	if err != nil {
		t.Fatalf("Spec with stale model: %v", err)
	}
	if got := spec.Env["HERMES_INFERENCE_MODEL"]; got != "gpt-5" {
		t.Errorf("stale model = %q, want the provider's first model", got)
	}

	// Errors: an unknown provider names the compatible ids, an incompatible
	// one says which API it lacks.
	if _, err := c.Spec(newGlobal(), "ghost", "", nil); err == nil || !strings.Contains(err.Error(), "openrouter") {
		t.Errorf("unknown provider: %v, want the compatible ids in the error", err)
	}
	if _, err := c.Spec(newGlobal(), "anthropic", "", nil); err == nil || !strings.Contains(err.Error(), "/v1/chat/completions") {
		t.Errorf("incompatible provider: %v, want the missing API named", err)
	}
}
