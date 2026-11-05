package bridges

import (
	"context"
	"fmt"
	"net"
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

// The Aperture redirects plain HTTP to HTTPS on its FQDN, and a client
// behind a Route cannot resolve that name because only the bridge's node is
// on the tailnet (tailscale/aperture#2974). Every request must ask for no
// redirect, whatever the client set.
func TestRouteAsksForNoRedirect(t *testing.T) {
	var got []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values("X-Aperture-No-Redirect")
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
	req.Header.Set("X-Aperture-No-Redirect", "false")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(got) != 1 || got[0] != "true" {
		t.Fatalf("X-Aperture-No-Redirect = %q, want [\"true\"]", got)
	}
}

// A serve binds the port it was given and fails when that port is taken
// (ADR 0008): a silent fallback to a free port would leave the client config
// pointing at nothing.
func TestRouteBindsAFixedPort(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free := held.Addr().(*net.TCPAddr).Port
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}

	node := &fakeNode{backendAddr: "100.64.0.2:80"}
	m := NewMachines(false)
	m.newNode = func(_ config.Bridge, _ int, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return node
	}
	defer m.Close()
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work"}
	mc, err := m.For(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := mc.Open(context.Background(), collect(&[]string{})); err != nil {
		t.Fatal(err)
	}

	route, err := mc.RouteTo(context.Background(), "http://ai", free, collect(&[]string{}))
	if err != nil {
		t.Fatalf("RouteTo on a free fixed port: %v", err)
	}
	if route.LocalURL != fmt.Sprintf("http://127.0.0.1:%d", free) {
		t.Errorf("LocalURL = %q, want the fixed port", route.LocalURL)
	}

	// A second Machine on a taken port fails rather than falls back.
	held, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	taken := held.Addr().(*net.TCPAddr).Port
	defer held.Close()
	m2 := NewMachines(false)
	m2.newNode = func(_ config.Bridge, _ int, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return &fakeNode{backendAddr: "100.64.0.2:80"}
	}
	defer m2.Close()
	mc2, err := m2.For(config.Bridge{ID: "bridge-fedcba", Name: "Home"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mc2.Open(context.Background(), collect(&[]string{})); err != nil {
		t.Fatal(err)
	}
	if _, err := mc2.RouteTo(context.Background(), "http://ai", taken, collect(&[]string{})); err == nil {
		t.Error("RouteTo bound a port another listener holds")
	}
}
