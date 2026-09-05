package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Canonical API paths advertised by Aperture's GET /v1/models endpoint.
const (
	EndpointAnthropicMessages = "/v1/messages"
	EndpointOpenAIResponses   = "/v1/responses"
	EndpointOpenAIChat        = "/v1/chat/completions"
	EndpointGemini            = "/v1beta/models/{model}:generateContent"
	EndpointVertexGemini      = "/v1/projects/{project}/locations/{region}/publishers/google/models/{model}:generateContent"
	EndpointVertexClaude      = "/v1/projects/{project}/locations/{region}/publishers/anthropic/models/{model}:rawPredict"
	EndpointBedrockInvoke     = "/bedrock/model/{model}/invoke"
	EndpointBedrockConverse   = "/bedrock/model/{model}/converse"
)

// ProviderInfo is the provider-level view aggregated from GET /v1/models.
type ProviderInfo struct {
	ID                 string
	Name               string
	Description        string
	Models             []string
	SupportedEndpoints map[string]bool
	Upstream           string
	RequiresClientAuth bool
}

// SupportsEndpoint reports whether Aperture advertises endpoint for this
// provider's models.
func (p ProviderInfo) SupportsEndpoint(endpoint string) bool {
	return p.SupportedEndpoints[endpoint]
}

// DisplayName returns the provider's Name, falling back to ID if Name is empty.
func (p ProviderInfo) DisplayName() string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}

type modelsResponse struct {
	Object string          `json:"object"`
	Data   json.RawMessage `json:"data"`
}

type modelEntry struct {
	ID                 string   `json:"id"`
	SupportedEndpoints []string `json:"supported_endpoints"`
	Metadata           struct {
		Provider modelProvider `json:"provider"`
	} `json:"metadata"`
}

type modelProvider struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	RequiresClientAuth bool   `json:"requires_client_auth"`
	Upstream           string `json:"upstream"`
}

// ParseProviders parses GET /v1/models and aggregates its model rows by
// provider. Provider and model order follow their first appearance.
func ParseProviders(data []byte) ([]ProviderInfo, error) {
	var response modelsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}
	if response.Object != "list" {
		return nil, fmt.Errorf("models response object is %q, want %q", response.Object, "list")
	}
	if response.Data == nil {
		return nil, fmt.Errorf("models response is missing data")
	}
	if bytes.Equal(bytes.TrimSpace(response.Data), []byte("null")) {
		return nil, fmt.Errorf("models response data must be an array")
	}

	var models []modelEntry
	if err := json.Unmarshal(response.Data, &models); err != nil {
		return nil, fmt.Errorf("decode models response data: %w", err)
	}

	var providers []ProviderInfo
	providerIndexes := make(map[string]int)
	seenModels := make(map[string]map[string]bool)
	for i, model := range models {
		if model.ID == "" {
			return nil, fmt.Errorf("model at data[%d] is missing id", i)
		}
		provider := model.Metadata.Provider
		if provider.ID == "" {
			return nil, fmt.Errorf("model %q is missing metadata.provider.id", model.ID)
		}
		if provider.Upstream == "" {
			return nil, fmt.Errorf("model %q provider %q is missing upstream", model.ID, provider.ID)
		}

		providerIndex, ok := providerIndexes[provider.ID]
		if !ok {
			providerIndex = len(providers)
			providerIndexes[provider.ID] = providerIndex
			seenModels[provider.ID] = make(map[string]bool)
			providers = append(providers, ProviderInfo{
				ID:                 provider.ID,
				Name:               provider.Name,
				Description:        provider.Description,
				SupportedEndpoints: make(map[string]bool),
				Upstream:           provider.Upstream,
				RequiresClientAuth: provider.RequiresClientAuth,
			})
		} else {
			existing := providers[providerIndex]
			if existing.Name != provider.Name ||
				existing.Description != provider.Description ||
				existing.Upstream != provider.Upstream ||
				existing.RequiresClientAuth != provider.RequiresClientAuth {
				return nil, fmt.Errorf("model %q has conflicting metadata for provider %q", model.ID, provider.ID)
			}
		}

		aggregated := &providers[providerIndex]
		if !seenModels[provider.ID][model.ID] {
			aggregated.Models = append(aggregated.Models, model.ID)
			seenModels[provider.ID][model.ID] = true
		}
		for _, endpoint := range model.SupportedEndpoints {
			if endpoint != "" {
				aggregated.SupportedEndpoints[endpoint] = true
			}
		}
	}
	return providers, nil
}
