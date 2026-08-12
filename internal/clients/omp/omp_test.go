package omp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

const testHost = "http://ai.example.com"

func backendByIDOrFatal(t *testing.T, id string) backend {
	t.Helper()
	b, ok := backendByID(id)
	if !ok {
		t.Fatalf("no backend with id %q", id)
	}
	return b
}

func TestBackendBaseURL(t *testing.T) {
	cases := map[string]string{
		"openai_responses": testHost + "/v1",
		"anthropic":        testHost,
		"openai_chat":      testHost + "/v1",
		"vertex":           testHost + "/v1/projects/_aperture_auto_vertex_project_id_/locations/_aperture_auto_vertex_region_/publishers/google",
	}
	for id, want := range cases {
		if got := backendByIDOrFatal(t, id).baseURL(testHost + "/"); got != want {
			t.Errorf("%s baseURL = %q, want %q", id, got, want)
		}
	}
}

func TestBuildProvider(t *testing.T) {
	p := config.ProviderInfo{ID: "openai-api", Models: []string{"gpt-5.6-sol"}}
	got := buildProvider(testHost, p, backendByIDOrFatal(t, "openai_responses"))
	if got.BaseURL != testHost+"/v1" || got.API != "openai-responses" || got.APIKey != "not-needed" {
		t.Errorf("buildProvider = %+v", got)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "gpt-5.6-sol" || got.Models[0].MaxTokens == 0 || len(got.Models[0].Input) == 0 {
		t.Errorf("models = %+v", got.Models)
	}
}

func TestExtensionSource(t *testing.T) {
	p := config.ProviderInfo{ID: "openai-api", Models: []string{"gpt-5.6-sol"}}
	src, err := extensionSource(p.ID, buildProvider(testHost, p, backendByIDOrFatal(t, "openai_responses")))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"export default function", "pi.registerProvider(", `"aperture-openai-api"`, `"openai-responses"`} {
		if !strings.Contains(src, want) {
			t.Errorf("extension missing %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "null") {
		t.Errorf("extension contains null:\n%s", src)
	}
}

func TestWriteProviderExtension(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	p := config.ProviderInfo{ID: "openai-api", Models: []string{"gpt-5.6-sol"}}
	path, cleanup, err := writeProviderExtension(testHost, p, backendByIDOrFatal(t, "openai_responses"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 600", info.Mode().Perm())
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("extension still exists after cleanup")
	}
}

func TestBuildArgs(t *testing.T) {
	want := []string{"-e", "/tmp/ext.js", "--model", "aperture-openai-api/gpt-5.6-sol", "--auto-approve"}
	got := buildArgs("/tmp/ext.js", "openai-api", "openai-api/gpt-5.6-sol", true)
	if !slices.Equal(got, want) {
		t.Errorf("buildArgs = %v, want %v", got, want)
	}
	if got := buildArgs("/tmp/ext.js", "openai-api", "", false); !slices.Equal(got, []string{"-e", "/tmp/ext.js"}) {
		t.Errorf("buildArgs without model = %v", got)
	}
}

func TestBackendsFor(t *testing.T) {
	p := config.ProviderInfo{Compatibility: map[string]bool{
		"openai_responses": true, "anthropic_messages": true, "openai_chat": true, "google_raw_predict": true,
	}}
	got := backendsFor(p)
	ids := make([]string, len(got))
	for i, b := range got {
		ids[i] = b.id
	}
	want := []string{"openai_responses", "anthropic", "openai_chat", "vertex"}
	if !slices.Equal(ids, want) {
		t.Errorf("backendsFor = %v, want %v", ids, want)
	}
}

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "match", Models: []string{"model"}, Compatibility: map[string]bool{"openai_responses": true}},
		{ID: "empty", Compatibility: map[string]bool{"openai_responses": true}},
		{ID: "other", Models: []string{"model"}, Compatibility: map[string]bool{"bedrock_converse": true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 1 || got[0].ID != "match" {
		t.Errorf("compatibleProviders = %+v", got)
	}
}

func TestResolveReplay(t *testing.T) {
	p := config.ProviderInfo{ID: "openai-api", Models: []string{"gpt-5.6-sol"}, Compatibility: map[string]bool{"openai_responses": true}}
	g := &config.Global{
		Providers:  []config.ProviderInfo{p},
		LastLaunch: config.LaunchState{LastClientName: name, LastBackendType: "openai_responses", LastProviderID: p.ID, LastModel: "openai-api/gpt-5.6-sol"},
	}
	_, _, model, ok := resolveReplay(g)
	if !ok || model != "openai-api/gpt-5.6-sol" {
		t.Fatalf("resolveReplay = %q, %v", model, ok)
	}
	g.LastLaunch.LastModel = "openai-api/stale"
	if _, _, _, ok := resolveReplay(g); ok {
		t.Error("resolveReplay accepted a stale model")
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
