package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "vertex", Compatibility: map[string]bool{"experimental_gemini_cli_vertex_compat": true}},
		{ID: "gemini", Compatibility: map[string]bool{"gemini_generate_content": true}},
		{ID: "openai", Compatibility: map[string]bool{"openai_chat": true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 2 {
		t.Fatalf("compatibleProviders len = %d, want 2", len(got))
	}
}

func TestBackendsFor_Vertex(t *testing.T) {
	p := config.ProviderInfo{Compatibility: map[string]bool{"experimental_gemini_cli_vertex_compat": true}}
	bs := backendsFor(p)
	if len(bs) != 1 || bs[0].id != "vertex" {
		t.Errorf("backendsFor(vertex) = %+v", bs)
	}
}

func TestBackendsFor_GeminiAPI(t *testing.T) {
	p := config.ProviderInfo{Compatibility: map[string]bool{"gemini_generate_content": true}}
	bs := backendsFor(p)
	if len(bs) != 1 || bs[0].id != "gemini" {
		t.Errorf("backendsFor(gemini) = %+v", bs)
	}
}

func TestBackendsFor_Both(t *testing.T) {
	p := config.ProviderInfo{Compatibility: map[string]bool{
		"experimental_gemini_cli_vertex_compat": true,
		"gemini_generate_content":               true,
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
		{"https://ai:8080", true},            // bare label
		{"http://ai.example.com", true},       // not https
		{"http://ai", true},                   // bare label + not https
		{"https://ai", true},                  // bare label
		{"ai.example.com", true},              // missing scheme
		{"https://", true},                    // missing host
		{"not a url", true},                   // unparseable
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
