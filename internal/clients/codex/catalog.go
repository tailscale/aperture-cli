package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
)

const modelCatalogReadTimeout = 5 * time.Second

// prepareModelCatalog asks Codex for its effective model catalog, adds aliases
// for the fully-qualified model IDs Aperture expects on the wire, and writes
// the result to a temporary file. The caller must run cleanup after Codex
// exits.
func prepareModelCatalog(codexBin string, providers []config.ProviderInfo) (path string, cleanup func(), err error) {
	catalog, err := readModelCatalog(codexBin)
	if err != nil {
		return "", nil, err
	}
	return writeModelCatalog(catalog, providers)
}

func writeModelCatalog(catalog []byte, providers []config.ProviderInfo) (path string, cleanup func(), err error) {
	catalog, added, err := augmentModelCatalog(catalog, providers)
	if err != nil {
		return "", nil, err
	}
	if added == 0 {
		return "", nil, nil
	}

	dir, err := os.MkdirTemp("", "aperture-codex-models-")
	if err != nil {
		return "", nil, fmt.Errorf("create temporary model catalog directory: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	path = filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, catalog, 0o600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write temporary model catalog: %w", err)
	}
	return path, cleanup, nil
}

// readModelCatalog returns the model catalog Codex would otherwise use. Using
// the effective catalog, rather than only the binary's bundled catalog,
// preserves user-supplied and remotely refreshed model metadata.
func readModelCatalog(codexBin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelCatalogReadTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, codexBin, "debug", "models").Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("read Codex model catalog: %w", ctx.Err())
		}
		return nil, fmt.Errorf("read Codex model catalog: %w", err)
	}
	return out, nil
}

// augmentModelCatalog clones metadata for known Codex models under the FQN
// aliases Aperture needs. Codex then recognizes the FQN while continuing to
// send that exact value as the request's model field.
func augmentModelCatalog(catalog []byte, providers []config.ProviderInfo) ([]byte, int, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(catalog, &document); err != nil {
		return nil, 0, fmt.Errorf("decode Codex model catalog: %w", err)
	}
	if document == nil {
		return nil, 0, fmt.Errorf("decode Codex model catalog: top level must be an object")
	}

	modelsJSON, ok := document["models"]
	if !ok || string(modelsJSON) == "null" {
		return nil, 0, fmt.Errorf("decode Codex model catalog: models must be an array")
	}
	var models []json.RawMessage
	if err := json.Unmarshal(modelsJSON, &models); err != nil {
		return nil, 0, fmt.Errorf("decode Codex model catalog models: %w", err)
	}

	bySlug := make(map[string]json.RawMessage, len(models))
	existingSlugs := make(map[string]bool, len(models))
	for _, model := range models {
		slug, ok := catalogModelSlug(model)
		if !ok {
			continue
		}
		existingSlugs[slug] = true
		if _, exists := bySlug[slug]; !exists {
			bySlug[slug] = model
		}
	}

	added := 0
	for _, provider := range providers {
		if provider.ID == "" || !provider.SupportsEndpoint(config.EndpointOpenAIResponses) {
			continue
		}
		for _, modelID := range provider.Models {
			if modelID == "" {
				continue
			}
			fqn := provider.ID + "/" + modelID
			if existingSlugs[fqn] {
				continue
			}

			source, ok := matchingCatalogModel(modelID, bySlug)
			if !ok {
				continue
			}
			alias, err := catalogModelAlias(source, fqn)
			if err != nil {
				return nil, 0, fmt.Errorf("create Codex model catalog alias %q: %w", fqn, err)
			}
			models = append(models, alias)
			existingSlugs[fqn] = true
			added++
		}
	}

	if added == 0 {
		return catalog, 0, nil
	}
	modelsJSON, err := json.Marshal(models)
	if err != nil {
		return nil, 0, fmt.Errorf("encode Codex model catalog models: %w", err)
	}
	document["models"] = modelsJSON
	out, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, 0, fmt.Errorf("encode Codex model catalog: %w", err)
	}
	return append(out, '\n'), added, nil
}

func catalogModelSlug(model json.RawMessage) (string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(model, &fields); err != nil {
		return "", false
	}
	var slug string
	if err := json.Unmarshal(fields["slug"], &slug); err != nil || slug == "" {
		return "", false
	}
	return slug, true
}

// matchingCatalogModel accepts exact model IDs and provider-qualified forms
// such as "openai.gpt-5.6-luna" or "openai/gpt-5.6-luna". Requiring a
// delimiter before the catalog slug avoids partial-name matches.
func matchingCatalogModel(modelID string, bySlug map[string]json.RawMessage) (json.RawMessage, bool) {
	if model, ok := bySlug[modelID]; ok {
		return model, true
	}

	var bestSlug string
	for slug := range bySlug {
		if len(slug) <= len(bestSlug) {
			continue
		}
		if strings.HasSuffix(modelID, "."+slug) || strings.HasSuffix(modelID, "/"+slug) {
			bestSlug = slug
		}
	}
	model, ok := bySlug[bestSlug]
	return model, ok
}

func catalogModelAlias(model json.RawMessage, slug string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(model, &fields); err != nil {
		return nil, err
	}
	slugJSON, err := json.Marshal(slug)
	if err != nil {
		return nil, err
	}
	fields["slug"] = slugJSON
	return json.Marshal(fields)
}
