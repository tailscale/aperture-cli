package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseProvidersAggregatesModels(t *testing.T) {
	input := []byte(`{
		"object": "list",
		"ignored": true,
		"data": [
			{
				"id": "first-model",
				"supported_endpoints": ["/v1/responses", "/v1/responses", "/unknown"],
				"metadata": {"provider": {
					"id": "first", "name": "First", "description": "one",
					"requires_client_auth": true, "upstream": "openai"
				}},
				"unknown": "ignored"
			},
			{
				"id": "shared-model",
				"supported_endpoints": ["/v1/messages"],
				"metadata": {"provider": {
					"id": "second", "name": "Second", "description": "two",
					"requires_client_auth": false, "upstream": "anthropic"
				}}
			},
			{
				"id": "second-model",
				"supported_endpoints": ["/v1/chat/completions", "/v1/responses"],
				"metadata": {"provider": {
					"id": "first", "name": "First", "description": "one",
					"requires_client_auth": true, "upstream": "openai"
				}}
			},
			{
				"id": "shared-model",
				"supported_endpoints": ["/v1/chat/completions"],
				"metadata": {"provider": {
					"id": "first", "name": "First", "description": "one",
					"requires_client_auth": true, "upstream": "openai"
				}}
			},
			{
				"id": "first-model",
				"supported_endpoints": ["/v1/responses"],
				"metadata": {"provider": {
					"id": "first", "name": "First", "description": "one",
					"requires_client_auth": true, "upstream": "openai"
				}}
			}
		]
	}`)

	got, err := ParseProviders(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []ProviderInfo{
		{
			ID: "first", Name: "First", Description: "one",
			Models: []string{"first-model", "second-model", "shared-model"},
			SupportedEndpoints: map[string]bool{
				EndpointOpenAIResponses: true,
				EndpointOpenAIChat:      true,
				"/unknown":              true,
			},
			Upstream: "openai", RequiresClientAuth: true,
		},
		{
			ID: "second", Name: "Second", Description: "two",
			Models:             []string{"shared-model"},
			SupportedEndpoints: map[string]bool{EndpointAnthropicMessages: true},
			Upstream:           "anthropic",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseProviders() = %#v, want %#v", got, want)
	}
}

func TestParseProvidersEmptyData(t *testing.T) {
	got, err := ParseProviders([]byte(`{"object":"list","data":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("ParseProviders() = %#v, want empty", got)
	}
}

func TestParseProvidersRejectsMalformedRows(t *testing.T) {
	provider := `"metadata":{"provider":{"id":"provider","name":"Provider","description":"desc","requires_client_auth":false,"upstream":"anthropic"}}`
	tests := []struct {
		name string
		data string
		want string
	}{
		{"wrong object", `{"object":"model","data":[]}`, "want \"list\""},
		{"missing data", `{"object":"list"}`, "missing data"},
		{"null data", `{"object":"list","data":null}`, "must be an array"},
		{"data is not an array", `{"object":"list","data":{}}`, "response data"},
		{"missing model ID", `{"object":"list","data":[{` + provider + `}]}`, "missing id"},
		{"missing provider ID", `{"object":"list","data":[{"id":"model","metadata":{"provider":{"upstream":"anthropic"}}}]}`, "provider.id"},
		{"missing upstream", `{"object":"list","data":[{"id":"model","metadata":{"provider":{"id":"provider"}}}]}`, "missing upstream"},
		{
			"conflicting provider metadata",
			`{"object":"list","data":[` +
				`{"id":"one",` + provider + `},` +
				`{"id":"two","metadata":{"provider":{"id":"provider","name":"Changed","description":"desc","requires_client_auth":false,"upstream":"anthropic"}}}` +
				`]}`,
			"conflicting metadata",
		},
		{
			"conflicting upstream",
			`{"object":"list","data":[` +
				`{"id":"one",` + provider + `},` +
				`{"id":"two","metadata":{"provider":{"id":"provider","name":"Provider","description":"desc","requires_client_auth":false,"upstream":"bedrock-mantle"}}}` +
				`]}`,
			"conflicting metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseProviders([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseProviders() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestProviderInfoSupportsEndpoint(t *testing.T) {
	p := ProviderInfo{SupportedEndpoints: map[string]bool{EndpointAnthropicMessages: true}}
	if !p.SupportsEndpoint(EndpointAnthropicMessages) {
		t.Error("SupportsEndpoint(/v1/messages) = false, want true")
	}
	if p.SupportsEndpoint("/unknown") {
		t.Error("SupportsEndpoint(/unknown) = true, want false")
	}
}
