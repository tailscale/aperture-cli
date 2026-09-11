package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
)

const modelCatalogReadTimeout = 5 * time.Second

// prepareModelCatalog asks Codex for its effective model catalog, adds aliases
// for the fully-qualified model IDs Aperture expects on the wire, and writes
// the result to a temporary file. The caller must run cleanup after Codex
// exits.
func prepareModelCatalog(codexBin string, providers []config.ProviderInfo, selectedProviderID string) (path string, cleanup func(), err error) {
	catalog, err := readModelCatalog(codexBin)
	if err != nil {
		return "", nil, err
	}
	return writeModelCatalog(catalog, providers, selectedProviderID)
}

func writeModelCatalog(catalog []byte, providers []config.ProviderInfo, selectedProviderID string) (path string, cleanup func(), err error) {
	updated, _, err := augmentModelCatalog(catalog, providers, selectedProviderID)
	if err != nil {
		return "", nil, err
	}
	if bytes.Equal(updated, catalog) {
		return "", nil, nil
	}
	catalog = updated

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
// send that exact value as the request's model field. Visible entries receive
// unique, dense priorities in provider-centric order: the launch-selected
// provider, the remaining provider IDs alphabetically, then native and
// otherwise unmanaged catalog entries.
func augmentModelCatalog(catalog []byte, providers []config.ProviderInfo, selectedProviderID string) ([]byte, int, error) {
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

	bySlug := make(map[string]int, len(models))
	existingSlugs := make(map[string]int, len(models))
	ranks := make([]catalogRank, len(models))
	for i, model := range models {
		ranks[i] = rankCatalogModel(model, i)
		slug, ok := catalogModelSlug(model)
		if !ok {
			continue
		}
		existingSlugs[slug] = i
		if _, exists := bySlug[slug]; !exists {
			bySlug[slug] = i
		}
	}

	added := 0
	changed := false
	var managed []catalogOrderItem
	managedIndexes := make(map[int]bool)
	managedSlugs := make(map[string]bool)
	for providerOrder, provider := range orderedCatalogProviders(providers, selectedProviderID) {
		for modelOrder, modelID := range provider.Models {
			if modelID == "" {
				continue
			}
			fqn := provider.ID + "/" + modelID
			if managedSlugs[fqn] {
				continue
			}

			sourceIndex, sourceOK := matchingCatalogModel(modelID, bySlug)
			targetIndex, exists := existingSlugs[fqn]
			if !exists {
				if !sourceOK {
					continue
				}
				alias, err := catalogModelAlias(models[sourceIndex], fqn)
				if err != nil {
					return nil, 0, fmt.Errorf("create Codex model catalog alias %q: %w", fqn, err)
				}
				targetIndex = len(models)
				models = append(models, alias)
				ranks = append(ranks, rankCatalogModel(alias, targetIndex))
				existingSlugs[fqn] = targetIndex
				added++
				changed = true
			}
			managedSlugs[fqn] = true
			if !catalogModelIsVisible(models[targetIndex]) {
				continue
			}

			rank := ranks[targetIndex]
			if sourceOK {
				rank = ranks[sourceIndex]
			}
			managed = append(managed, catalogOrderItem{
				index:         targetIndex,
				rank:          rank,
				providerOrder: providerOrder,
				modelOrder:    modelOrder,
				slug:          fqn,
			})
			managedIndexes[targetIndex] = true
		}
	}

	if added == 0 && len(managed) == 0 {
		return catalog, 0, nil
	}

	if len(managed) > 0 {
		sort.SliceStable(managed, func(i, j int) bool {
			a, b := managed[i], managed[j]
			if a.providerOrder != b.providerOrder {
				return a.providerOrder < b.providerOrder
			}
			if lessCatalogRank(a.rank, b.rank) {
				return true
			}
			if lessCatalogRank(b.rank, a.rank) {
				return false
			}
			if a.modelOrder != b.modelOrder {
				return a.modelOrder < b.modelOrder
			}
			return a.slug < b.slug
		})

		var unmanaged []catalogOrderItem
		for i, model := range models {
			if managedIndexes[i] || !catalogModelIsVisible(model) {
				continue
			}
			slug, ok := catalogModelSlug(model)
			if !ok {
				continue
			}
			unmanaged = append(unmanaged, catalogOrderItem{index: i, rank: ranks[i], slug: slug})
		}
		sort.SliceStable(unmanaged, func(i, j int) bool {
			a, b := unmanaged[i], unmanaged[j]
			if lessCatalogRank(a.rank, b.rank) {
				return true
			}
			if lessCatalogRank(b.rank, a.rank) {
				return false
			}
			return a.slug < b.slug
		})

		ordered := append(managed, unmanaged...)
		for i, item := range ordered {
			priority := int64(i + 1)
			if current, ok := catalogModelPriority(models[item.index]); ok && current == priority {
				continue
			}
			model, err := catalogModelWithPriority(models[item.index], i+1)
			if err != nil {
				return nil, 0, fmt.Errorf("set Codex model catalog priority for %q: %w", item.slug, err)
			}
			models[item.index] = model
			changed = true
		}
	}
	if !changed {
		return catalog, added, nil
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

type catalogRank struct {
	priority int64
	index    int
}

type catalogOrderItem struct {
	index         int
	rank          catalogRank
	providerOrder int
	modelOrder    int
	slug          string
}

func orderedCatalogProviders(providers []config.ProviderInfo, selectedProviderID string) []config.ProviderInfo {
	ordered := make([]config.ProviderInfo, 0, len(providers))
	for _, provider := range providers {
		if provider.ID != "" && provider.SupportsEndpoint(config.EndpointOpenAIResponses) {
			ordered = append(ordered, provider)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		aSelected := a.ID == selectedProviderID
		bSelected := b.ID == selectedProviderID
		if aSelected != bSelected {
			return aSelected
		}
		aID, bID := strings.ToLower(a.ID), strings.ToLower(b.ID)
		if aID != bID {
			return aID < bID
		}
		return a.ID < b.ID
	})
	return ordered
}

func rankCatalogModel(model json.RawMessage, index int) catalogRank {
	rank := catalogRank{priority: int64(index), index: index}
	if priority, ok := catalogModelPriority(model); ok {
		rank.priority = priority
	}
	return rank
}

func catalogModelPriority(model json.RawMessage) (int64, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(model, &fields); err != nil {
		return 0, false
	}
	var priority int64
	if err := json.Unmarshal(fields["priority"], &priority); err != nil {
		return 0, false
	}
	return priority, true
}

func lessCatalogRank(a, b catalogRank) bool {
	if a.priority != b.priority {
		return a.priority < b.priority
	}
	return a.index < b.index
}

func catalogModelIsVisible(model json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(model, &fields); err != nil {
		return false
	}
	var visibility string
	if err := json.Unmarshal(fields["visibility"], &visibility); err != nil {
		return true
	}
	return visibility != "hide"
}

func catalogModelWithPriority(model json.RawMessage, priority int) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(model, &fields); err != nil {
		return nil, err
	}
	priorityJSON, err := json.Marshal(priority)
	if err != nil {
		return nil, err
	}
	fields["priority"] = priorityJSON
	return json.Marshal(fields)
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
func matchingCatalogModel(modelID string, bySlug map[string]int) (int, bool) {
	if index, ok := bySlug[modelID]; ok {
		return index, true
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
	index, ok := bySlug[bestSlug]
	return index, ok
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
