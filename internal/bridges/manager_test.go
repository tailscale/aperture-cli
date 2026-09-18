package bridges

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
	"tailscale.com/health"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

type fakeNode struct {
	backendAddr string
	status      *ipnstate.Status
	statusFn    func() (*ipnstate.Status, error)
	watchFn     func(ev events)
	upFn        func()
	upErr       error
	statusErr   error
	dialErr     error
	dialFn      bridgeDialFunc
	logoutErr   error
	up          int
	loggedOut   int
	closed      bool

	mu     sync.Mutex
	dialed []string
}

func (n *fakeNode) Up(context.Context) (*ipnstate.Status, error) {
	n.up++
	if n.upFn != nil {
		n.upFn()
	}
	return n.status, n.upErr
}

func (n *fakeNode) Status(context.Context) (*ipnstate.Status, error) {
	if n.statusFn != nil {
		return n.statusFn()
	}
	return n.status, n.statusErr
}

func (n *fakeNode) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	n.mu.Lock()
	n.dialed = append(n.dialed, address)
	n.mu.Unlock()
	if n.dialFn != nil {
		return n.dialFn(ctx, network, n.backendAddr)
	}
	if n.dialErr != nil {
		return nil, n.dialErr
	}
	var d net.Dialer
	return d.DialContext(ctx, network, n.backendAddr)
}

// WatchLogin stands in for the IPN bus watch: watchFn is what a test wants the
// bus to report, and it runs until the manager cancels the watch.
func (n *fakeNode) WatchLogin(ctx context.Context, ev events) {
	if n.watchFn != nil {
		n.watchFn(ev)
	}
	<-ctx.Done()
}

// collect records what a bridge reported, rendered the way the connect screen
// renders it, so an assertion reads like the line the user would have seen.
func collect(lines *[]string) func(connection.Event) {
	return func(ev connection.Event) { *lines = append(*lines, ev.String()) }
}

// collectLocked is collect for the tests whose events arrive off a watch
// goroutine.
func collectLocked(mu *sync.Mutex, lines *[]string) func(connection.Event) {
	return func(ev connection.Event) {
		mu.Lock()
		defer mu.Unlock()
		*lines = append(*lines, ev.String())
	}
}

func (n *fakeNode) dialedAddrs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.dialed...)
}

// tailnetStatus is a node status that knows one peer, the shape every dial
// through a bridge depends on.
func tailnetStatus(dnsName string, addrs ...string) *ipnstate.Status {
	_, suffix, _ := strings.Cut(strings.TrimSuffix(dnsName, "."), ".")
	ips := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, netip.MustParseAddr(addr))
	}
	return &ipnstate.Status{
		BackendState:   "Running",
		TailscaleIPs:   []netip.Addr{netip.MustParseAddr("100.64.0.1")},
		CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: suffix},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			key.NewNode().Public(): {DNSName: dnsName, TailscaleIPs: ips},
		},
	}
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
	m.peerWait, m.peerWaitInterval = 5*time.Millisecond, time.Millisecond
	m.newNode = func(_ config.Bridge, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return node
	}
	defer m.Close()

	var logs []string
	localURL, err := m.Activate(
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://aperture",
		collect(&logs),
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
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			key.NewNode().Public(): {
				DNSName:      "aperture.example.ts.net.",
				TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")},
			},
		},
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
		collect(&logs),
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

// TestActivateWaitsForPeerMapBeforeDialing covers the first-connection hang: a
// node reports Running before its peer map lands, and dialing the target's name
// in that window escapes to the host resolver.
func TestActivateWaitsForPeerMapBeforeDialing(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()

	var polls atomic.Int32
	node := &fakeNode{backendAddr: strings.TrimPrefix(backend.URL, "http://")}
	node.statusFn = func() (*ipnstate.Status, error) {
		if polls.Add(1) < 3 {
			return &ipnstate.Status{BackendState: "Running"}, nil
		}
		return tailnetStatus("ai.example.ts.net.", "100.64.0.2"), nil
	}

	m := NewManager(true)
	m.peerWaitInterval = time.Millisecond
	m.newNode = func(_ config.Bridge, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return node
	}
	defer m.Close()

	var logs []string
	localURL, err := m.Activate(
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://ai",
		collect(&logs),
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
	if got := node.dialedAddrs(); len(got) != 1 || got[0] != "100.64.0.2:80" {
		t.Fatalf("dialed %v, want one dial to 100.64.0.2:80", got)
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "remote=") {
		t.Fatalf("logs missing the connected dial:\n%s", got)
	}
}

// TestActivateLogsLoginLinkWhileUpBlocks covers the bridge that looked hung: a
// node that has never logged in blocks in Up until someone visits a link, so
// the link has to reach the log while Up is still blocked, not after it.
func TestActivateLogsLoginLinkWhileUpBlocks(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()

	const url = "https://login.tailscale.com/a/28ba393017981"
	watched := make(chan struct{})
	node := &fakeNode{
		backendAddr: backend.Listener.Addr().String(),
		status:      tailnetStatus("ai.example.ts.net.", "100.64.0.2"),
	}
	node.watchFn = func(ev events) {
		link, err := connection.ParseLoginLink(url)
		if err != nil {
			t.Error(err)
			return
		}
		ev.login(link)
		close(watched)
	}
	// Up stands in for the wait on an interactive login, and gives up so a
	// manager that never watches fails the assertion instead of hanging.
	node.upFn = func() {
		select {
		case <-watched:
		case <-time.After(2 * time.Second):
		}
	}

	m := NewManager(false)
	m.newNode = func(_ config.Bridge, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return node
	}
	defer m.Close()

	var mu sync.Mutex
	var logs []string
	if _, err := m.Activate(
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://ai",
		collectLocked(&mu, &logs),
	); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, line := range logs {
		if strings.Contains(line, url) {
			return
		}
	}
	t.Errorf("login link never reached the activation log:\n%s", strings.Join(logs, "\n"))
}

func TestDialViaNode(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	backendAddr := backend.Listener.Addr().String()
	discard := events(func(connection.Event) {})

	t.Run("dials the address the node's peer map gives", func(t *testing.T) {
		node := &fakeNode{backendAddr: backendAddr, status: tailnetStatus("ai.example.ts.net.", "100.64.0.2")}

		conn, attempts, err := dialViaNode(
			context.Background(), node, "tcp", "ai:80", discard, time.Second, time.Millisecond,
		)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if attempts != 1 {
			t.Errorf("status polls = %d, want 1", attempts)
		}
		if got := node.dialedAddrs(); len(got) != 1 || got[0] != "100.64.0.2:80" {
			t.Errorf("dialed %v, want [100.64.0.2:80]", got)
		}
	})

	t.Run("waits for the peer map to arrive", func(t *testing.T) {
		var polls int
		node := &fakeNode{backendAddr: backendAddr}
		node.statusFn = func() (*ipnstate.Status, error) {
			polls++
			if polls < 3 {
				return &ipnstate.Status{BackendState: "Running"}, nil
			}
			return tailnetStatus("ai.example.ts.net.", "100.64.0.2"), nil
		}

		conn, attempts, err := dialViaNode(
			context.Background(), node, "tcp", "ai:80", discard, time.Second, time.Millisecond,
		)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if attempts != 3 {
			t.Errorf("status polls = %d, want 3", attempts)
		}
		if got := node.dialedAddrs(); len(got) != 1 || got[0] != "100.64.0.2:80" {
			t.Errorf("dialed %v, want [100.64.0.2:80], never the bare name", got)
		}
	})

	t.Run("falls back to the name when the target is not a peer", func(t *testing.T) {
		node := &fakeNode{backendAddr: backendAddr, status: tailnetStatus("other.example.ts.net.", "100.64.0.3")}
		var logs []string

		conn, _, err := dialViaNode(
			context.Background(), node, "tcp", "ai:80",
			collect(&logs),
			5*time.Millisecond, time.Millisecond,
		)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if got := node.dialedAddrs(); len(got) != 1 || got[0] != "ai:80" {
			t.Errorf("dialed %v, want [ai:80]", got)
		}
		if got := strings.Join(logs, "\n"); !strings.Contains(got, "not a node on this bridge's tailnet") {
			t.Errorf("logs do not say the target left the tailnet's DNS:\n%s", got)
		}
	})

	t.Run("dials an IP target without asking for status", func(t *testing.T) {
		node := &fakeNode{backendAddr: backendAddr, statusFn: func() (*ipnstate.Status, error) {
			t.Error("status polled for an address that needs no resolving")
			return nil, nil
		}}

		conn, _, err := dialViaNode(
			context.Background(), node, "tcp", "100.64.0.2:80", discard, time.Second, time.Millisecond,
		)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if got := node.dialedAddrs(); len(got) != 1 || got[0] != "100.64.0.2:80" {
			t.Errorf("dialed %v, want [100.64.0.2:80]", got)
		}
	})

	t.Run("stops when activation is canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		node := &fakeNode{backendAddr: backendAddr}
		node.statusFn = func() (*ipnstate.Status, error) {
			cancel()
			return &ipnstate.Status{BackendState: "Running"}, nil
		}

		_, attempts, err := dialViaNode(ctx, node, "tcp", "ai:80", discard, time.Second, time.Millisecond)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
		if attempts != 1 {
			t.Errorf("status polls = %d, want 1", attempts)
		}
		if got := node.dialedAddrs(); len(got) != 0 {
			t.Errorf("dialed %v after cancellation, want nothing", got)
		}
	})
}

func TestPeerAddr(t *testing.T) {
	tests := []struct {
		name   string
		status *ipnstate.Status
		host   string
		want   string
		wantOK bool
	}{
		{
			name:   "short name matches the first label",
			status: tailnetStatus("ai.example.ts.net.", "100.64.0.2"),
			host:   "ai",
			want:   "100.64.0.2",
			wantOK: true,
		},
		{
			name:   "full MagicDNS name matches",
			status: tailnetStatus("ai.example.ts.net.", "100.64.0.2"),
			host:   "AI.example.ts.net",
			want:   "100.64.0.2",
			wantOK: true,
		},
		{
			name:   "IPv4 wins over IPv6",
			status: tailnetStatus("ai.example.ts.net.", "fd7a:115c:a1e0::2", "100.64.0.2"),
			host:   "ai",
			want:   "100.64.0.2",
			wantOK: true,
		},
		{
			name:   "IPv6-only peer still resolves",
			status: tailnetStatus("ai.example.ts.net.", "fd7a:115c:a1e0::2"),
			host:   "ai",
			want:   "fd7a:115c:a1e0::2",
			wantOK: true,
		},
		{
			name:   "another tailnet's node is not a match",
			status: tailnetStatus("other.example.ts.net.", "100.64.0.3"),
			host:   "ai",
		},
		{
			name: "no status at all",
			host: "ai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := peerAddr(tt.status, tt.host)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.String() != tt.want {
				t.Errorf("addr = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestActivateRecordsTailnet(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()

	m := NewManager(false)
	m.newNode = func(_ config.Bridge, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		return &fakeNode{
			backendAddr: backend.Listener.Addr().String(),
			status:      &ipnstate.Status{CurrentTailnet: &ipnstate.TailnetStatus{Name: "corp.example.com"}},
		}
	}
	defer m.Close()

	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work"}
	if _, err := m.Activate(context.Background(), bridge, "http://aperture.tailnet", nil); err != nil {
		t.Fatal(err)
	}
	if got := m.Tailnet(bridge.ID); got != "corp.example.com" {
		t.Errorf("Tailnet = %q, want corp.example.com", got)
	}
}

// TestSwitchTailnet covers what makes a switch a switch: the node is logged out
// rather than just restarted, so the next connection has to ask for a login.
func TestSwitchTailnet(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()

	f := activate(t, backend)
	defer f.manager.Close()
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work"}
	f.manager.tailnets[bridge.ID] = "corp.example.com"
	first := f.node

	if err := f.manager.SwitchTailnet(context.Background(), bridge, nil); err != nil {
		t.Fatal(err)
	}
	if first.loggedOut != 1 {
		t.Errorf("logouts = %d, want 1", first.loggedOut)
	}
	if !first.closed {
		t.Error("node was not closed")
	}
	if got := f.manager.Tailnet(bridge.ID); got != "" {
		t.Errorf("Tailnet = %q, want empty after a switch", got)
	}
	if _, err := http.Get(f.localURL + "/"); err == nil {
		t.Error("proxy still serving after the bridge was logged out")
	}

	var replacement *fakeNode
	f.manager.newNode = func(_ config.Bridge, _ string, _ func(string, ...any), _ func(string, ...any)) tailnetNode {
		replacement = &fakeNode{backendAddr: backend.Listener.Addr().String()}
		return replacement
	}
	if _, err := f.manager.Activate(context.Background(), bridge, "http://aperture.tailnet", nil); err != nil {
		t.Fatal(err)
	}
	if replacement == nil {
		t.Fatal("Activate reused the logged-out node")
	}
	if replacement.up != 1 {
		t.Errorf("replacement node Up calls = %d, want 1", replacement.up)
	}
}

func TestSwitchTailnetReportsLogoutFailure(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()

	f := activate(t, backend)
	defer f.manager.Close()
	f.node.logoutErr = errors.New("not logged in")

	err := f.manager.SwitchTailnet(context.Background(), config.Bridge{ID: "bridge-abcdef", Name: "Work"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v, want it to name the logout failure", err)
	}
}

func (n *fakeNode) Logout(context.Context) error {
	n.loggedOut++
	return n.logoutErr
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
		f.node = &fakeNode{
			backendAddr: backend.Listener.Addr().String(),
			status:      tailnetStatus("aperture.tailnet.", "100.64.0.2"),
		}
		return f.node
	}

	var err error
	f.localURL, err = f.manager.Activate(
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://aperture.tailnet",
		collect(&f.logs),
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

// state and browse are the two notifications the bus sends that this package
// translates. Named here rather than inline so the tests read as the sequence
// a real login produces.
func state(s ipn.State) *ipn.Notify { return &ipn.Notify{State: &s} }
func browse(u string) *ipn.Notify   { return &ipn.Notify{BrowseToURL: &u} }

// TestLoginReporterSplitsTheTwoNeedsLoginWaits is the diagnosis this change
// came from. A 29 second bridge said only that it needed a login, so there was
// no telling the control plane not having answered from a user who had not
// finished in the browser. Both are ipn.NeedsLogin; here they are two phases.
func TestLoginReporterSplitsTheTwoNeedsLoginWaits(t *testing.T) {
	const url = "https://login.tailscale.com/a/28ba393017981"
	var got []string
	r := &loginReporter{ev: collect(&got)}

	r.notify(state(ipn.NeedsLogin))
	r.notify(state(ipn.NeedsLogin)) // the bus repeats itself
	r.notify(browse(url))
	r.notify(state(ipn.NeedsLogin)) // still NeedsLogin, but no longer that wait
	r.notify(browse(url))           // and the same link again
	r.notify(state(ipn.Starting))
	r.notify(state(ipn.Running))

	want := []string{
		connection.AwaitingLoginLink.String(),
		connection.AwaitingAuthorization.String(),
		"Authorize this bridge at " + url,
		"Authorize this bridge at " + url,
		connection.JoiningTailnet.String(),
		connection.FindingEndpoint.String(),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("reported:\n%s\n\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestLoginReporterRejectsAnUnusableLink(t *testing.T) {
	var got []string
	r := &loginReporter{ev: collect(&got)}

	// http, not https. The value is handed to a desktop opener, so this is the
	// one thing that must not pass through untouched.
	r.notify(browse("http://evil.example.com/a/x"))

	if len(got) != 1 || !strings.Contains(got[0], "unusable login link") {
		t.Fatalf("reported %q, want one line saying the link was ignored", got)
	}
	if strings.Contains(got[0], "Authorize this bridge at") {
		t.Errorf("an http link was offered to the browser: %q", got[0])
	}
}

// TestLoginReporterNamesTheWaitBeforeTheControlPlaneAnswers is the state the
// first version missed. A bridge that never logged in sits in ipn.NoState for
// the whole of POST /machine/register, so NoState is the wait this exists to
// name. Untranslated it emits nothing and the screen goes silent.
func TestLoginReporterNamesTheWaitBeforeTheControlPlaneAnswers(t *testing.T) {
	var lines []string
	reporter := loginReporter{ev: collect(&lines)}

	// NoState alone, which is all the user gets for the length of the
	// register. NeedsLogin arrives only once control has answered, so a test
	// that ends on it would pass on the NeedsLogin case and prove nothing.
	reporter.notify(state(ipn.NoState))
	want := []string{connection.AwaitingLoginLink.String()}
	if !slices.Equal(lines, want) {
		t.Fatalf("reported %q, want %q", lines, want)
	}

	// And the NeedsLogin that follows it is the same wait, not a second one.
	reporter.notify(state(ipn.NoState))
	reporter.notify(state(ipn.NeedsLogin))
	if !slices.Equal(lines, want) {
		t.Errorf("reported %q, want the wait named once", lines)
	}
}

// TestAProxyReportsToTheAttemptUsingItNow covers a defect the string logger had
// too: proxies are cached for the life of the process, attempts are not, and
// startProxy's closures captured whichever attempt created the proxy. Every
// later dial failure went to a channel nobody had read since.
func TestAProxyReportsToTheAttemptUsingItNow(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()

	f := activate(t, backend)
	defer f.manager.Close()

	// A second attempt on the same bridge and target, which reuses both the
	// node and the proxy the first one built.
	var mu sync.Mutex
	var second []string
	if _, err := f.manager.Activate(
		context.Background(),
		config.Bridge{ID: "bridge-abcdef", Name: "Work"},
		"http://aperture.tailnet",
		collectLocked(&mu, &second),
	); err != nil {
		t.Fatal(err)
	}

	backend.Close() // the bridge breaking under a connection that already worked
	// The proxy answers 502 rather than failing the request, so the status is
	// what says the dial underneath it did not happen.
	res, err := http.Get(f.localURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d with no backend behind the proxy", res.StatusCode, http.StatusBadGateway)
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.ContainsFunc(second, func(s string) bool { return strings.Contains(s, "dial failed") }) {
		t.Errorf("the attempt using the proxy was told nothing; it saw %q", second)
	}
	if slices.ContainsFunc(f.logs, func(s string) bool { return strings.Contains(s, "dial failed") }) {
		t.Errorf("the finished attempt was still being written to: %q", f.logs)
	}
}

// TestSinkLogsEveryEvent covers the case the run log exists for: a connect
// attempt that gets killed. Whatever the screen was showing is gone with the
// process, so every event has to reach the file on its way to the screen,
// including on the paths that pass no screen sink at all.
func TestSinkLogsEveryEvent(t *testing.T) {
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	link, err := connection.ParseLoginLink("https://login.tailscale.com/a/17bceb7b0129ba")
	if err != nil {
		t.Fatal(err)
	}

	var seen []connection.Event
	ev := sink(func(e connection.Event) { seen = append(seen, e) })
	ev.enter(connection.StartingMachine)
	ev.login(link)
	ev.note("dialing")
	if len(seen) != 3 {
		t.Errorf("screen saw %d events, want the tee to forward all 3", len(seen))
	}

	// The nil sink is the reuse path, which still has to leave a record.
	sink(nil).enter(connection.FindingEndpoint)

	logged := buf.String()
	for _, want := range []string{
		connection.StartingMachine.String(),
		"bridge needs login",
		"dialing",
		connection.FindingEndpoint.String(),
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("run log = %q, want it to record %q", logged, want)
		}
	}
	if strings.Contains(logged, link.String()) {
		t.Error("run log contains the authorization capability")
	}
}

// TestNotifyLogsWhatTheScreenCollapses is the 43 second kill: the connect
// screen reported "Waiting for a login link" and then nothing, which is the
// same picture whether the register is slow, the link was thrown away, or the
// watch died. The screen merges those on purpose. The run log must not.
func TestNotifyLogsWhatTheScreenCollapses(t *testing.T) {
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	var lines []string
	r := &loginReporter{ev: collect(&lines)}
	r.notify(state(ipn.NoState))
	r.notify(state(ipn.NeedsLogin))
	r.notify(browse("http://evil.example.com/a/x"))

	logged := buf.String()
	// Both states, though the screen shows one phase for the pair: which one
	// the attempt is stuck in is the difference between waiting on control and
	// waiting on the user.
	for _, want := range []string{ipn.NoState.String(), ipn.NeedsLogin.String()} {
		if !strings.Contains(logged, want) {
			t.Errorf("run log = %q, want the raw state %q", logged, want)
		}
	}
	// At Info, not behind -debug: the note this pairs with is a debug note, so
	// without this a discarded link is invisible on the run that hit it.
	if !strings.Contains(logged, "login link is not https") {
		t.Errorf("run log = %q, want the rejection reason", logged)
	}
	if strings.Contains(logged, "http://evil.example.com/a/x") {
		t.Error("run log contains the rejected authorization URL")
	}
}

func unhealthyLogin(text string) *ipn.Notify {
	return &ipn.Notify{Health: &health.State{
		Warnings: map[health.WarnableCode]health.UnhealthyState{
			health.LoginStateWarnable.Code: {WarnableCode: health.LoginStateWarnable.Code, Text: text},
		},
	}}
}

// TestLoginReporterReportsALoginThatIsFailing is the 502 register loop. The
// node stays in NeedsLogin and never sends a BrowseToURL, so the attempt sits
// on "Waiting for a login link" while tsnet retries. The error is not a
// vizerror, so ErrMessage stays nil and health state is the only place it is.
func TestLoginReporterReportsALoginThatIsFailing(t *testing.T) {
	const text = "You are logged out. The last login error was: register request: http 502"

	var lines []string
	r := &loginReporter{ev: collect(&lines)}
	r.notify(state(ipn.NeedsLogin))
	r.notify(unhealthyLogin(text))

	if len(lines) != 2 || !strings.Contains(lines[1], text) {
		t.Fatalf("reported %q, want the wait followed by why it will not end", lines)
	}

	// Every retry re-sends the state with a fresh request ID in the text, about
	// once a second. Reporting each one would push the phases off the screen.
	r.notify(unhealthyLogin(text + " REQ-0001"))
	r.notify(unhealthyLogin(text + " REQ-0002"))
	if len(lines) != 2 {
		t.Errorf("reported %q, want the failure named once while it lasts", lines)
	}

	// A retry that succeeds clears the warning, and the next failure is news
	// again rather than a repeat.
	r.notify(&ipn.Notify{Health: &health.State{}})
	r.notify(unhealthyLogin(text))
	if len(lines) != 3 {
		t.Errorf("reported %q, want a failure after a recovery to be reported", lines)
	}
}

// TestLoginReporterIgnoresWarningsThatAreNotTheLogin keeps the connect screen
// about the wait it is in. A node with no DERP home is a real warning and not
// this attempt's business.
func TestLoginReporterIgnoresWarningsThatAreNotTheLogin(t *testing.T) {
	var lines []string
	r := &loginReporter{ev: collect(&lines)}
	r.notify(&ipn.Notify{Health: &health.State{
		Warnings: map[health.WarnableCode]health.UnhealthyState{
			"no-derp-home": {WarnableCode: "no-derp-home", Text: "no home DERP"},
		},
	}})
	if len(lines) != 0 {
		t.Errorf("reported %q, want an unrelated warning left off the connect screen", lines)
	}
}
