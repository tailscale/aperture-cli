package portals

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

func TestActivateStartsReverseProxy(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "aperture.tailnet" {
			t.Errorf("Host = %q, want aperture.tailnet", r.Host)
		}
		if r.URL.Path != "/api/providers" {
			t.Errorf("path = %q, want /api/providers", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"anthropic"}]`))
	}))
	defer backend.Close()

	var node *fakeNode
	m := NewManager(false)
	m.newNode = func(config.Portal, string, func(string, ...any), func(string, ...any)) tailnetNode {
		node = &fakeNode{backendAddr: backend.Listener.Addr().String()}
		return node
	}

	var logs []string
	localURL, err := m.Activate(context.Background(), config.Portal{ID: "portal-abcdef", Name: "Work"}, "http://aperture.tailnet", func(line string) {
		logs = append(logs, line)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(localURL, "http://127.0.0.1:") {
		t.Fatalf("localURL = %q, want localhost URL", localURL)
	}

	resp, err := http.Get(localURL + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `[{"id":"anthropic"}]` {
		t.Errorf("body = %s", body)
	}
	if node.up != 1 {
		t.Errorf("Up called %d times, want 1", node.up)
	}
	if len(logs) == 0 {
		t.Error("expected activation logs")
	}

	localURL2, err := m.Activate(context.Background(), config.Portal{ID: "portal-abcdef", Name: "Work"}, "http://aperture.tailnet", nil)
	if err != nil {
		t.Fatal(err)
	}
	if localURL2 != localURL {
		t.Errorf("reused localURL = %q, want %q", localURL2, localURL)
	}
	if node.up != 1 {
		t.Errorf("Up called %d times after reuse, want 1", node.up)
	}

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if !node.closed {
		t.Error("node was not closed")
	}
}
