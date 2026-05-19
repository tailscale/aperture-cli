package bridges

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

type fakeNode struct {
	backendAddr string
	up          int
	closed      bool
}

func (n *fakeNode) Up(context.Context) error {
	n.up++
	return nil
}

func (n *fakeNode) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, n.backendAddr)
}

func (n *fakeNode) Close() error {
	n.closed = true
	return nil
}

// activatedManager creates a Manager with a fake node wired to backend,
// activates the bridge once, and returns everything tests need.
type activatedFixture struct {
	manager  *Manager
	node     *fakeNode
	localURL string
	logs     []string
}

func activate(t *testing.T, backend *httptest.Server) activatedFixture {
	t.Helper()
	var f activatedFixture
	f.manager = NewManager(false)
	f.manager.newNode = func(_ config.Bridge, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		f.node = &fakeNode{backendAddr: backend.Listener.Addr().String()}
		return f.node
	}

	var err error
	f.localURL, err = f.manager.Activate(
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://aperture.tailnet",
		func(line string) { f.logs = append(f.logs, line) },
	)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestActivate(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "proxies requests to backend",
			run: func(t *testing.T) {
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(`[{"id":"anthropic"}]`))
				}))
				defer backend.Close()

				f := activate(t, backend)
				defer f.manager.Close()

				resp, err := http.Get(f.localURL + "/api/providers")
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if got := string(body); got != `[{"id":"anthropic"}]` {
					t.Errorf("body = %s, want %s", got, `[{"id":"anthropic"}]`)
				}
			},
		},
		{
			name: "rewrites Host header to target",
			run: func(t *testing.T) {
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Host != "aperture.tailnet" {
						t.Errorf("Host = %q, want aperture.tailnet", r.Host)
					}
				}))
				defer backend.Close()

				f := activate(t, backend)
				defer f.manager.Close()

				resp, err := http.Get(f.localURL + "/")
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
			},
		},
		{
			name: "forwards request path",
			run: func(t *testing.T) {
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/api/providers" {
						t.Errorf("path = %q, want /api/providers", r.URL.Path)
					}
				}))
				defer backend.Close()

				f := activate(t, backend)
				defer f.manager.Close()

				resp, err := http.Get(f.localURL + "/api/providers")
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
			},
		},
		{
			name: "returns localhost URL and calls Up once",
			run: func(t *testing.T) {
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
				defer backend.Close()

				f := activate(t, backend)
				defer f.manager.Close()

				if !strings.HasPrefix(f.localURL, "http://127.0.0.1:") {
					t.Fatalf("localURL = %q, want http://127.0.0.1:... prefix", f.localURL)
				}
				if f.node.up != 1 {
					t.Errorf("Up called %d times, want 1", f.node.up)
				}
				if len(f.logs) == 0 {
					t.Error("expected activation logs")
				}
			},
		},
		{
			name: "reuses existing bridge without calling Up again",
			run: func(t *testing.T) {
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
				defer backend.Close()

				f := activate(t, backend)
				defer f.manager.Close()

				localURL2, err := f.manager.Activate(
					context.Background(),
					config.Bridge{ID: "bridge-abcdef", Name: "Work"},
					"http://aperture.tailnet",
					nil,
				)
				if err != nil {
					t.Fatal(err)
				}
				if localURL2 != f.localURL {
					t.Errorf("reused localURL = %q, want %q", localURL2, f.localURL)
				}
				if f.node.up != 1 {
					t.Errorf("Up called %d times after reuse, want 1", f.node.up)
				}
			},
		},
		{
			name: "Close shuts down node",
			run: func(t *testing.T) {
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
				defer backend.Close()

				f := activate(t, backend)

				if err := f.manager.Close(); err != nil {
					t.Fatal(err)
				}
				if !f.node.closed {
					t.Error("node was not closed")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}
