package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

	got, added, err := augmentModelCatalog(catalog, providers, "mantle")
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
	delete(luna, "priority")
	delete(lunaAlias, "slug")
	delete(lunaAlias, "priority")
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

	got, added, err := augmentModelCatalog(catalog, providers, "router")
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

	got, added, err := augmentModelCatalog(catalog, providers, "custom")
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

func TestAugmentModelCatalogOrdersVisibleModelsByProvider(t *testing.T) {
	catalog := []byte(`{
  "models": [
    {"slug":"model-b","priority":20,"metadata":"native b"},
    {"slug":"user-short","priority":15},
    {"slug":"model-a","priority":10,"metadata":"native a"},
    {"slug":"hidden","priority":0,"visibility":"hide"},
    {"slug":"alpha/model-b","priority":99,"metadata":"existing alias","custom":true}
  ]
}`)
	providers := []config.ProviderInfo{
		{
			ID:                 "zeta",
			Models:             []string{"model-b", "model-a"},
			SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
		},
		{
			ID:                 "middle",
			Models:             []string{"vendor.model-b", "model-a"},
			SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
		},
		{
			ID:                 "alpha",
			Models:             []string{"model-b", "model-a"},
			SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
		},
		{
			ID:                 "chat-only",
			Models:             []string{"model-a"},
			SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true},
		},
	}

	got, added, err := augmentModelCatalog(catalog, providers, "middle")
	if err != nil {
		t.Fatal(err)
	}
	if added != 5 {
		t.Fatalf("added = %d, want 5", added)
	}

	document := decodeCatalog(t, got)
	wantOrder := []string{
		"middle/model-a",
		"middle/vendor.model-b",
		"alpha/model-a",
		"alpha/model-b",
		"zeta/model-a",
		"zeta/model-b",
		"model-a",
		"user-short",
		"model-b",
	}
	if got := document.visiblePriorityOrder(t); !reflect.DeepEqual(got, wantOrder) {
		t.Errorf("visible priority order:\n got: %q\nwant: %q", got, wantOrder)
	}
	for priority, slug := range wantOrder {
		if got := document.priority(t, slug); got != priority+1 {
			t.Errorf("priority for %q = %d, want %d", slug, got, priority+1)
		}
	}
	if got := document.priority(t, "hidden"); got != 0 {
		t.Errorf("hidden model priority = %d, want 0", got)
	}
	alphaB := document.model(t, "alpha/model-b")
	if alphaB["metadata"] != "existing alias" || alphaB["custom"] != true {
		t.Errorf("existing alias metadata was not preserved: %#v", alphaB)
	}
	if document.hasModel("chat-only/model-a") {
		t.Error("chat-only provider unexpectedly received a Codex alias")
	}
}

func TestAugmentModelCatalogReordersExistingAliases(t *testing.T) {
	catalog := []byte(`{"models":[
  {"slug":"model-b","priority":1},
  {"slug":"selected/model-b","priority":3},
  {"slug":"model-a","priority":2},
  {"slug":"selected/model-a","priority":4}
]}`)
	providers := []config.ProviderInfo{{
		ID:                 "selected",
		Models:             []string{"model-b", "model-a"},
		SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
	}}

	got, added, err := augmentModelCatalog(catalog, providers, "selected")
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("added = %d, want 0", added)
	}
	wantOrder := []string{"selected/model-b", "selected/model-a", "model-b", "model-a"}
	if got := decodeCatalog(t, got).visiblePriorityOrder(t); !reflect.DeepEqual(got, wantOrder) {
		t.Errorf("visible priority order:\n got: %q\nwant: %q", got, wantOrder)
	}

	path, cleanup, err := writeModelCatalog(catalog, providers, "selected")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" || cleanup == nil {
		t.Fatal("writeModelCatalog did not write priority-only catalog changes")
	}
	t.Cleanup(cleanup)
}

func TestAugmentModelCatalogLeavesOrderedExistingAliasesAlone(t *testing.T) {
	catalog := []byte("{\n  \"models\": [\n    {\"slug\":\"selected/model\",\"priority\":1},\n    {\"slug\":\"model\",\"priority\":2}\n  ]\n}\n")
	providers := []config.ProviderInfo{{
		ID:                 "selected",
		Models:             []string{"model"},
		SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true},
	}}

	got, added, err := augmentModelCatalog(catalog, providers, "selected")
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("added = %d, want 0", added)
	}
	if !bytes.Equal(got, catalog) {
		t.Error("catalog changed even though aliases and priorities were already current")
	}
	path, cleanup, err := writeModelCatalog(catalog, providers, "selected")
	if err != nil {
		t.Fatal(err)
	}
	if path != "" || cleanup != nil {
		t.Error("writeModelCatalog wrote an unchanged catalog")
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
			if _, _, err := augmentModelCatalog(catalog, nil, ""); err == nil {
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

	path, cleanup, err := writeModelCatalog(catalog, providers, "mantle")
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

func (c testCatalog) priority(t *testing.T, slug string) int {
	t.Helper()
	model := c.model(t, slug)
	priority, ok := model["priority"].(float64)
	if !ok {
		t.Fatalf("model %q has invalid priority %#v", slug, model["priority"])
	}
	return int(priority)
}

func (c testCatalog) visiblePriorityOrder(t *testing.T) []string {
	t.Helper()
	type prioritySlug struct {
		priority int
		slug     string
	}
	var models []prioritySlug
	for _, model := range c.Models {
		if model["visibility"] == "hide" {
			continue
		}
		slug, ok := model["slug"].(string)
		if !ok || slug == "" {
			continue
		}
		models = append(models, prioritySlug{priority: c.priority(t, slug), slug: slug})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].priority < models[j].priority })
	order := make([]string, len(models))
	for i, model := range models {
		order[i] = model.slug
	}
	return order
}
