package bridges

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"tailscale.com/ipn/ipnstate"
)

type fakeNode struct {
	backendAddr string
	status      *ipnstate.Status
	upErr       error
	statusErr   error
	dialErr     error
	dialFn      bridgeDialFunc
	up          int
	closed      bool
}

func (n *fakeNode) Up(context.Context) (*ipnstate.Status, error) {
	n.up++
	return n.status, n.upErr
}

func (n *fakeNode) Status(context.Context) (*ipnstate.Status, error) {
	return n.status, n.statusErr
}

func (n *fakeNode) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	if n.dialFn != nil {
		return n.dialFn(ctx, network, n.backendAddr)
	}
	if n.dialErr != nil {
		return nil, n.dialErr
	}
	var d net.Dialer
	return d.DialContext(ctx, network, n.backendAddr)
}

func TestActivateDebugDiagnostics(t *testing.T) {
	status := &ipnstate.Status{
		BackendState: "Running",
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		Self:         &ipnstate.PeerStatus{DNSName: "aperture-cli.example.ts.net."},
		CurrentTailnet: &ipnstate.TailnetStatus{
			Name:            "example.com",
			MagicDNSSuffix:  "example.ts.net",
			MagicDNSEnabled: true,
		},
	}
	node := &fakeNode{status: status, dialErr: errors.New("lookup aperture on 127.0.0.53:53: no such host")}
	m := NewManager(true)
	m.newNode = func(_ config.Bridge, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return node
	}
	defer m.Close()

	var logs []string
	localURL, err := m.Activate(
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://aperture",
		func(line string) { logs = append(logs, line) },
	)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(localURL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	if !strings.Contains(string(body), "lookup aperture") {
		t.Errorf("response body = %q, want dial error", body)
	}

	got := strings.Join(logs, "\n")
	for _, want := range []string{
		`tailnet="example.com"`,
		`dns_suffix="example.ts.net"`,
		`target is not present among visible peers`,
		`Bridge dial failed`,
		`lookup aperture`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("logs missing %q:\n%s", want, got)
		}
	}
}

func TestActivateClosesNodeWhenUpFails(t *testing.T) {
	node := &fakeNode{upErr: errors.New("login failed")}
	m := NewManager(false)
	m.newNode = func(_ config.Bridge, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return node
	}

	_, err := m.Activate(
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://ai",
		nil,
	)
	if !errors.Is(err, node.upErr) {
		t.Fatalf("Activate error = %v, want %v", err, node.upErr)
	}
	if !node.closed {
		t.Error("node was not closed after Up failure")
	}
}

func TestActivateNormalLoggingOmitsDebugDiagnostics(t *testing.T) {
	status := &ipnstate.Status{
		BackendState: "Running",
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		Self:         &ipnstate.PeerStatus{DNSName: "aperture-cli.example.ts.net."},
		CurrentTailnet: &ipnstate.TailnetStatus{
			Name:            "example.com",
			MagicDNSSuffix:  "example.ts.net",
			MagicDNSEnabled: true,
		},
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	backendAddr := strings.TrimPrefix(backend.URL, "http://")
	node := &fakeNode{status: status, backendAddr: backendAddr}
	m := NewManager(false)
	m.newNode = func(_ config.Bridge, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return node
	}
	defer m.Close()

	var logs []string
	localURL, err := m.Activate(
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://aperture",
		func(line string) { logs = append(logs, line) },
	)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(localURL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	got := strings.Join(logs, "\n")
	for _, unwanted := range []string{"Bridge network:", "Bridge health:", "Bridge target ", "Bridge dialing", "Bridge dial connected:"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("normal logs contain debug diagnostic %q:\n%s", unwanted, got)
		}
	}
}

func TestActivateRetriesDNSWhilePeerMapArrives(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	var attempts atomic.Int32
	node := &fakeNode{
		backendAddr: strings.TrimPrefix(backend.URL, "http://"),
		status: &ipnstate.Status{
			BackendState: "Running",
			TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		},
	}
	node.dialFn = func(ctx context.Context, network, address string) (net.Conn, error) {
		if attempts.Add(1) == 1 {
			return nil, &net.DNSError{
				Err:         "server misbehaving",
				Name:        "ai",
				Server:      "127.0.0.53:53",
				IsTemporary: true,
			}
		}
		var d net.Dialer
		return d.DialContext(ctx, network, address)
	}

	m := NewManager(true)
	m.newNode = func(_ config.Bridge, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return node
	}
	defer m.Close()

	var logs []string
	localURL, err := m.Activate(
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://ai",
		func(line string) { logs = append(logs, line) },
	)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(localURL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("dial attempts = %d, want 2", got)
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "attempts=2") {
		t.Fatalf("logs missing recovered dial attempt count:\n%s", got)
	}
}

func TestDialWithDNSRetry(t *testing.T) {
	t.Run("recovers when embedded DNS receives the target", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer backend.Close()

		attempts := 0
		dial := func(ctx context.Context, network, address string) (net.Conn, error) {
			attempts++
			if attempts == 1 {
				return nil, &net.DNSError{
					Err:         "server misbehaving",
					Name:        "ai",
					Server:      "127.0.0.53:53",
					IsTemporary: true,
				}
			}
			var d net.Dialer
			return d.DialContext(ctx, network, strings.TrimPrefix(backend.URL, "http://"))
		}

		conn, gotAttempts, err := dialWithDNSRetry(
			context.Background(), dial, "tcp", "ai:80", time.Second, time.Millisecond,
		)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if gotAttempts != 2 {
			t.Fatalf("attempts = %d, want 2", gotAttempts)
		}
	})

	t.Run("stops when the retry window expires", func(t *testing.T) {
		wantErr := &net.DNSError{Err: "server misbehaving", Name: "ai"}
		attempts := 0
		dial := func(context.Context, string, string) (net.Conn, error) {
			attempts++
			return nil, wantErr
		}

		_, gotAttempts, err := dialWithDNSRetry(
			context.Background(), dial, "tcp", "ai:80", 5*time.Millisecond, time.Hour,
		)
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		if gotAttempts < 2 || gotAttempts != attempts {
			t.Fatalf("attempts = %d/%d, want at least 2 matching attempts", gotAttempts, attempts)
		}
	})

	t.Run("does not retry non-DNS failures", func(t *testing.T) {
		wantErr := errors.New("connection refused")
		attempts := 0
		dial := func(context.Context, string, string) (net.Conn, error) {
			attempts++
			return nil, wantErr
		}

		_, gotAttempts, err := dialWithDNSRetry(
			context.Background(), dial, "tcp", "ai:80", time.Second, time.Millisecond,
		)
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		if gotAttempts != 1 || attempts != 1 {
			t.Fatalf("attempts = %d/%d, want 1/1", gotAttempts, attempts)
		}
	})

	t.Run("stops when activation is canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		dial := func(context.Context, string, string) (net.Conn, error) {
			attempts++
			cancel()
			return nil, &net.DNSError{Err: "server misbehaving", Name: "ai"}
		}

		_, gotAttempts, err := dialWithDNSRetry(
			ctx, dial, "tcp", "ai:80", time.Second, time.Second,
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
		if gotAttempts != 1 || attempts != 1 {
			t.Fatalf("attempts = %d/%d, want 1/1", gotAttempts, attempts)
		}
	})
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

				resp, err := http.Get(f.localURL + "/v1/models")
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
					if r.URL.Path != "/v1/models" {
						t.Errorf("path = %q, want /v1/models", r.URL.Path)
					}
				}))
				defer backend.Close()

				f := activate(t, backend)
				defer f.manager.Close()

				resp, err := http.Get(f.localURL + "/v1/models")
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
