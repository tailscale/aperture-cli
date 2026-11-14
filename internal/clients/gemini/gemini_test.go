package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "vertex", SupportedEndpoints: map[string]bool{config.EndpointVertexGemini: true}},
		{ID: "gemini", SupportedEndpoints: map[string]bool{config.EndpointGemini: true}},
		{ID: "openai", SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 2 {
		t.Fatalf("compatibleProviders len = %d, want 2", len(got))
	}
}

func TestBackendsFor_Vertex(t *testing.T) {
	p := config.ProviderInfo{SupportedEndpoints: map[string]bool{config.EndpointVertexGemini: true}}
	bs := backendsFor(p)
	if len(bs) != 1 || bs[0].id != "vertex" {
		t.Errorf("backendsFor(vertex) = %+v", bs)
	}
}

func TestBackendsFor_GeminiAPI(t *testing.T) {
	p := config.ProviderInfo{SupportedEndpoints: map[string]bool{config.EndpointGemini: true}}
	bs := backendsFor(p)
	if len(bs) != 1 || bs[0].id != "gemini" {
		t.Errorf("backendsFor(gemini) = %+v", bs)
	}
}

func TestBackendsFor_Both(t *testing.T) {
	p := config.ProviderInfo{SupportedEndpoints: map[string]bool{
		config.EndpointVertexGemini: true,
		config.EndpointGemini:       true,
	}}
	bs := backendsFor(p)
	if len(bs) != 2 {
		t.Errorf("backendsFor = %+v, want 2", bs)
	}
}

func TestValidateHost(t *testing.T) {
	cases := []struct {
		host    string
		wantErr bool
	}{
		{"https://ai.example.com", false},
		{"https://aperture.corp.ts.net/", false},
		{"https://ai:8080", true},       // bare label
		{"http://ai.example.com", true}, // not https
		{"http://ai", true},             // bare label + not https
		{"https://ai", true},            // bare label
		{"ai.example.com", true},        // missing scheme
		{"https://", true},              // missing host
		{"not a url", true},             // unparseable
	}
	for _, c := range cases {
		err := validateHost(c.host)
		if (err != nil) != c.wantErr {
			t.Errorf("validateHost(%q) err=%v, wantErr=%v", c.host, err, c.wantErr)
		}
	}
}

func TestWriteConfig_Vertex(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	home, err := writeConfig("vertex-ai")
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	if err != nil {
		t.Fatalf("settings.json: %v", err)
	}
	var s struct {
		Security struct {
			Auth struct {
				SelectedType string `json:"selectedType"`
			} `json:"auth"`
		} `json:"security"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	if s.Security.Auth.SelectedType != "vertex-ai" {
		t.Errorf("selectedType = %q, want vertex-ai", s.Security.Auth.SelectedType)
	}
}

// The headless launch (ADR 0007): flags win, LastLaunch fills what they
// leave empty, and anything left goes to the first compatible provider.
func TestSpec(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("APPDATA", tmp)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	openai := config.ProviderInfo{ID: "openai", SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}}
	vertexOnly := config.ProviderInfo{ID: "vertex", SupportedEndpoints: map[string]bool{config.EndpointVertexGemini: true}}
	both := config.ProviderInfo{ID: "both", SupportedEndpoints: map[string]bool{
		config.EndpointVertexGemini: true,
		config.EndpointGemini:       true,
	}}
	newGlobal := func() *config.Global {
		return &config.Global{ApertureHost: "https://ai.example.com", Providers: []config.ProviderInfo{openai, vertexOnly, both}}
	}
	c := &Client{}
	argsContain := func(args []string, want ...string) bool {
		for i := range len(args) - len(want) + 1 {
			if slices.Equal(args[i:i+len(want)], want) {
				return true
			}
		}
		return false
	}

	// Defaults: the first compatible provider, its first backend, and no
	// --model so the CLI picks its own.
	spec, err := c.Spec(newGlobal(), "", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if spec.Env["GOOGLE_VERTEX_BASE_URL"] == "" || len(spec.Args) != 0 {
		t.Errorf("default env %v args %v, want the vertex backend and no flags", spec.Env, spec.Args)
	}

	// Flags win, and passthrough args land after aperture's own.
	spec, err = c.Spec(newGlobal(), "both", "gemini-2.5-pro", []string{"--checkpointing"})
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if !argsContain(spec.Args, "--model", "gemini-2.5-pro") || spec.Args[len(spec.Args)-1] != "--checkpointing" {
		t.Errorf("flag args = %v, want the flag model and the passthrough last", spec.Args)
	}

	// LastLaunch fills empty flags, including the backend it recorded.
	g := newGlobal()
	g.LastLaunch = config.LaunchState{LastClientName: name, LastProviderID: "both", LastBackendType: "gemini", LastModel: "gemini-2.5-flash"}
	spec, err = c.Spec(g, "", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if spec.Env["GEMINI_BASE_URL"] == "" || !argsContain(spec.Args, "--model", "gemini-2.5-flash") {
		t.Errorf("replay env %v args %v, want LastLaunch's backend and model", spec.Env, spec.Args)
	}

	// LastLaunch from another client, or naming a provider that is gone,
	// is ignored.
	g = newGlobal()
	g.LastLaunch = config.LaunchState{LastClientName: "OpenAI Codex", LastProviderID: "both", LastBackendType: "gemini", LastModel: "gpt-5"}
	spec, err = c.Spec(g, "", "", nil)
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	if spec.Env["GOOGLE_VERTEX_BASE_URL"] == "" || len(spec.Args) != 0 {
		t.Errorf("other client's state leaked: env %v args %v", spec.Env, spec.Args)
	}

	// Errors: an unknown provider names the compatible ids, an
	// incompatible one says which APIs it lacks.
	if _, err := c.Spec(newGlobal(), "ghost", "", nil); err == nil || !strings.Contains(err.Error(), "vertex, both") {
		t.Errorf("unknown provider: %v, want the compatible ids in the error", err)
	}
	if _, err := c.Spec(newGlobal(), "openai", "", nil); err == nil || !strings.Contains(err.Error(), "Vertex Gemini or the Gemini API") {
		t.Errorf("incompatible provider: %v, want the missing APIs named", err)
	}
}
