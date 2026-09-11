package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

func TestAugmentModelCatalog(t *testing.T) {
	catalog := []byte(`{
  "models": [
    {
      "slug": "gpt-5.6-luna",
      "display_name": "GPT-5.6-Luna",
      "context_window": 1050000,
      "future_metadata": {"preserved": true}
    },
    {
      "slug": "gpt-5.6-sol",
      "display_name": "GPT-5.6-Sol"
    },
    {
      "slug": "mantle/already-present",
      "display_name": "Existing alias"
    },
    {"entry_without_a_slug": true}
  ],
  "future_top_level": {"preserved": true}
}`)
	providers := []config.ProviderInfo{
		{
			ID:                 "mantle",
			Models:             []string{"openai.gpt-5.6-luna", "unknown", "already-present"},
			SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
		},
		{
			ID:                 "direct",
			Models:             []string{"gpt-5.6-sol"},
			SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
		},
		{
			ID:                 "chat-only",
			Models:             []string{"gpt-5.6-luna"},
			SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true},
		},
	}

	got, added, err := augmentModelCatalog(catalog, providers)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}

	document := decodeCatalog(t, got)
	if len(document.Models) != 6 {
		t.Fatalf("models = %d, want 6", len(document.Models))
	}
	if !document.FutureTopLevel.Preserved {
		t.Error("future top-level catalog data was not preserved")
	}

	luna := document.model(t, "gpt-5.6-luna")
	lunaAlias := document.model(t, "mantle/openai.gpt-5.6-luna")
	delete(luna, "slug")
	delete(lunaAlias, "slug")
	if !reflect.DeepEqual(lunaAlias, luna) {
		t.Errorf("Luna alias metadata differs from source:\n got: %#v\nwant: %#v", lunaAlias, luna)
	}

	document.model(t, "direct/gpt-5.6-sol")
	if document.hasModel("mantle/unknown") {
		t.Error("unknown model unexpectedly received fallback metadata")
	}
	if document.hasModel("chat-only/gpt-5.6-luna") {
		t.Error("chat-only provider unexpectedly received a Codex alias")
	}
}

func TestAugmentModelCatalogMatchesSlashQualifiedModel(t *testing.T) {
	catalog := []byte(`{"models":[{"slug":"gpt-5.6-luna","context_window":1050000}]}`)
	providers := []config.ProviderInfo{{
		ID:                 "router",
		Models:             []string{"openai/gpt-5.6-luna"},
		SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
	}}

	got, added, err := augmentModelCatalog(catalog, providers)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	decodeCatalog(t, got).model(t, "router/openai/gpt-5.6-luna")
}

func TestAugmentModelCatalogLeavesUnknownModelsAlone(t *testing.T) {
	catalog := []byte("{\n  \"models\": [{\"slug\": \"gpt-5.6-luna\"}]\n}\n")
	providers := []config.ProviderInfo{{
		ID:                 "custom",
		Models:             []string{"not-gpt-5.6-luna"},
		SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
	}}

	got, added, err := augmentModelCatalog(catalog, providers)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("added = %d, want 0", added)
	}
	if !bytes.Equal(got, catalog) {
		t.Error("catalog changed even though no safe aliases were found")
	}
}

func TestAugmentModelCatalogRejectsInvalidCatalog(t *testing.T) {
	tests := map[string][]byte{
		"invalid JSON":   []byte(`{`),
		"null":           []byte(`null`),
		"missing models": []byte(`{}`),
		"null models":    []byte(`{"models":null}`),
		"object models":  []byte(`{"models":{}}`),
	}
	for name, catalog := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := augmentModelCatalog(catalog, nil); err == nil {
				t.Fatal("augmentModelCatalog unexpectedly succeeded")
			}
		})
	}
}

func TestWriteModelCatalogLifecycle(t *testing.T) {
	catalog := []byte(`{"models":[{"slug":"gpt-5.6-luna","context_window":1050000}]}`)
	providers := []config.ProviderInfo{{
		ID:                 "mantle",
		Models:             []string{"openai.gpt-5.6-luna"},
		SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
	}}

	path, cleanup, err := writeModelCatalog(catalog, providers)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("writeModelCatalog returned an empty path")
	}
	if cleanup == nil {
		t.Fatal("writeModelCatalog returned a nil cleanup function")
	}
	dir := filepath.Dir(path)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Errorf("catalog mode = %v, want %v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decodeCatalog(t, data).model(t, "mantle/openai.gpt-5.6-luna")

	cleanup()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary catalog directory still exists after cleanup: %v", err)
	}
}

type testCatalog struct {
	Models         []map[string]any `json:"models"`
	FutureTopLevel struct {
		Preserved bool `json:"preserved"`
	} `json:"future_top_level"`
}

func decodeCatalog(t *testing.T, data []byte) testCatalog {
	t.Helper()
	var catalog testCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func (c testCatalog) hasModel(slug string) bool {
	for _, model := range c.Models {
		if model["slug"] == slug {
			return true
		}
	}
	return false
}

func (c testCatalog) model(t *testing.T, slug string) map[string]any {
	t.Helper()
	for _, model := range c.Models {
		if model["slug"] == slug {
			return model
		}
	}
	t.Fatalf("model %q not found", slug)
	return nil
}
