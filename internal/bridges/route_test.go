package bridges

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

// TestRouteDropsClientAuthorization reproduces the Solar Winds 401: every
// client aperture-cli launches sends a placeholder bearer because its SDK
// refuses to build a request without one, and a gateway that treats that
// header as authoritative rejects it. The Route reaches an Aperture whose
// ingress auth is the Machine's tailnet identity, so the header is never
// load-bearing and must not cross. The backend stands in for their gateway.
func TestRouteDropsClientAuthorization(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, `{"code":"invalid_api_key","message":"Invalid bearer token"}`, http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	node := &fakeNode{backendAddr: strings.TrimPrefix(backend.URL, "http://")}
	m := NewMachines(false)
	m.newNode = func(_ config.Bridge, _ int, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return node
	}
	defer m.Close()

	localURL, err := activateMachine(m,
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://127.0.0.1",
		collect(&[]string{}),
	)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, localURL+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer not-needed")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d: Authorization crossed the Route", resp.StatusCode, http.StatusNoContent)
	}
}
