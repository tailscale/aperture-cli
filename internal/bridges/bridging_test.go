package bridges

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
)

func TestFetchProvidersIncludesErrorResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bridge proxy error: lookup aperture", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := fetchProviders(context.Background(), srv.URL, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "lookup aperture") {
		t.Fatalf("fetchProviders error = %v, want response detail", err)
	}
}

func TestFetchProvidersUsesModelsEndpoint(t *testing.T) {
	srv := modelsServerWithHandler(t, func(r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); got != "aperture-cli" {
			t.Errorf("User-Agent = %q, want aperture-cli", got)
		}
	})

	got, err := fetchProviders(context.Background(), srv.URL+"/", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "anthropic" || !got[0].SupportsEndpoint(config.EndpointAnthropicMessages) {
		t.Fatalf("fetchProviders() = %#v, want Anthropic Messages provider", got)
	}
}

func TestFetchProvidersHonorsCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := fetchProviders(ctx, srv.URL, time.Minute)
		result <- err
	}()
	<-requestStarted
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("fetchProviders error = %v, want context canceled", err)
	}
}

func modelsServerWithHandler(t *testing.T, check func(*http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[{
				"id":"claude-opus-5",
				"supported_endpoints":["/v1/messages"],
				"metadata":{"provider":{
					"id":"anthropic","name":"Anthropic","description":"",
					"requires_client_auth":false,"upstream":"anthropic"
				}}
			}]
		}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ParseEndpointURL accepts a query, so a saved endpoint can carry one. The
// models path has to go on the path, not after the query.
func TestFetchProvidersKeepsTheEndpointQuery(t *testing.T) {
	srv := modelsServerWithHandler(t, func(r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.URL.Query().Get("token"); got != "x" {
			t.Errorf("token = %q, want the endpoint's query kept", got)
		}
	})

	if _, err := fetchProviders(context.Background(), srv.URL+"?token=x", time.Minute); err != nil {
		t.Fatal(err)
	}
}
