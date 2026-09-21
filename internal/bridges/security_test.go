package bridges

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func TestPeerAddrScopesShortNames(t *testing.T) {
	for _, tt := range []struct {
		name, host, suffix, want string
		local                    bool
	}{
		{name: "foreign short name", host: "ai", suffix: "work-tail.ts.net"},
		{name: "unknown suffix", host: "ai"},
		{name: "local short name wins", host: "ai", suffix: "work-tail.ts.net", local: true, want: "100.64.0.2"},
		{name: "normalized suffix", host: "AI.", suffix: "WORK-TAIL.TS.NET.", local: true, want: "100.64.0.2"},
		{name: "explicit shared peer", host: "AI.attacker-tail.ts.net.", suffix: "work-tail.ts.net", want: "100.64.0.99"},
		{name: "explicit peer without suffix", host: "ai.attacker-tail.ts.net", want: "100.64.0.99"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status := tailnetStatus("ai.attacker-tail.ts.net.", "100.64.0.99")
			status.CurrentTailnet = nil
			if tt.suffix != "" {
				status.CurrentTailnet = &ipnstate.TailnetStatus{MagicDNSSuffix: tt.suffix}
			}
			if tt.local {
				status.Peer[key.NewNode().Public()] = &ipnstate.PeerStatus{
					DNSName: "ai.work-tail.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")},
				}
			}
			for range 64 { // Map iteration must never choose the foreign short alias.
				got, ok := peerAddr(status, tt.host)
				if ok != (tt.want != "") || ok && got.String() != tt.want {
					t.Fatalf("peerAddr(%q) = %v, %v; want %q", tt.host, got, ok, tt.want)
				}
			}
		})
	}
}

type sharedPeerNode struct {
	*fakeNode
	backend string
}

func (n *sharedPeerNode) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if address != "100.64.0.99:80" {
		return nil, fmt.Errorf("no work-tail peer at %s", address)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, n.backend)
}

func TestProxyRequiresExplicitSharedPeerName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const payload = "synthetic-private-prompt"
	received := make(chan string, 2)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	status := tailnetStatus("ai.attacker-tail.ts.net.", "100.64.0.99")
	status.CurrentTailnet = &ipnstate.TailnetStatus{MagicDNSSuffix: "work-tail.ts.net"}
	node := &sharedPeerNode{fakeNode: &fakeNode{status: status}, backend: backend.Listener.Addr().String()}
	m := NewMachines(false)
	m.peerWait = 0
	m.newNode = func(config.Bridge, string, func(string, ...any), func(string, ...any)) tailnetNode { return node }
	defer m.Close()
	for _, target := range []string{"http://ai", "http://ai.attacker-tail.ts.net"} {
		t.Run(target, func(t *testing.T) {
			localURL, err := activateMachine(m, context.Background(), config.Bridge{ID: "bridge-abcdef", Name: "Work"}, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Timeout: time.Second}
			resp, err := client.Post(localURL+"/v1/chat/completions", "application/json", strings.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if target == "http://ai" {
				if resp.StatusCode != http.StatusBadGateway {
					t.Errorf("bare name status = %d, want a failed dial", resp.StatusCode)
				}
				select {
				case body := <-received:
					t.Errorf("shared-in peer received data intended for work-tail's ai: %q", body)
				default:
				}
			} else {
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("explicit peer status = %d", resp.StatusCode)
				}
				if body := <-received; body != payload {
					t.Errorf("explicit peer received %q", body)
				}
			}
		})
	}
}

func TestRunLogOmitsLoginCapabilities(t *testing.T) {
	const secret = "synthetic-login-token"
	const authURL = "https://login.tailscale.com/a/" + secret
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		for _, source := range []string{"login event", "rejected link", "health warning", "backend and startup error"} {
			t.Run(level.String()+"/"+source, func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				f, err := config.OpenRunLog()
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})))
				defer slog.SetDefault(previous)
				switch source {
				case "login event":
					link, err := connection.ParseLoginLink(authURL)
					if err != nil {
						t.Fatal(err)
					}
					var shown *connection.LoginLink
					sink(func(e connection.Event) { shown = e.Link })(connection.LoginRequired(link))
					if shown.String() != authURL {
						t.Fatal("interactive consumer lost the authorization URL")
					}
				case "rejected link":
					r := loginReporter{ev: sink(nil)}
					r.notify(browse("http://login.tailscale.com/a/" + secret))
				case "health warning":
					r := loginReporter{ev: sink(nil)}
					r.notify(unhealthyLogin("request failed: " + authURL))
				case "backend and startup error":
					m := NewMachines(true)
					m.newNode = func(_ config.Bridge, _ string, userLogf, debugLogf func(string, ...any)) tailnetNode {
						userLogf("To authenticate, visit: %s", authURL)
						debugLogf("Received auth URL: %q", "HTTPS://login.tailscale.com/a/"+secret)
						return &fakeNode{upErr: errors.New("authorization failed at " + authURL)}
					}
					_, _ = activateMachine(m, context.Background(), config.Bridge{ID: "bridge-abcdef"}, "http://ai", nil)
					_ = m.Close()
				}
				data, err := os.ReadFile(f.Name())
				if err != nil {
					t.Fatal(err)
				}
				if len(data) == 0 {
					t.Fatal("diagnostic event was lost instead of redacted")
				}
				if strings.Contains(string(data), secret) {
					t.Errorf("persistent %s log contains the authorization capability", level)
				}
			})
		}
	}
}

// A bare name that is not in this Machine's peer map is not handed to tsnet
// to resolve. Its resolver falls through to the host resolver, which on a
// machine already on another tailnet answers with that tailnet's node of the
// same name, and the five second wait only delayed that. A name with a dot
// cannot be mistaken for a peer alias, so a subnet router or the tailnet's
// own DNS may still serve it.
func TestDialRefusesAnUnknownShortName(t *testing.T) {
	node := &fakeNode{status: tailnetStatus("other.work-tail.ts.net.", "100.64.0.7")}
	_, _, err := dialViaNode(context.Background(), node, "tcp", "ai:80", sink(nil), 0, 0)
	if err == nil {
		t.Fatal("dial of an unknown short name succeeded")
	}
	if dialed := node.dialedAddrs(); len(dialed) != 0 {
		t.Errorf("short name handed to tsnet: dialed %v", dialed)
	}

	node.dialErr = errors.New("no route")
	_, _, err = dialViaNode(context.Background(), node, "tcp", "db.internal.example:5432", sink(nil), 0, 0)
	if err == nil || !errors.Is(err, node.dialErr) {
		t.Fatalf("qualified name err = %v, want the node's own dial", err)
	}
	if dialed := node.dialedAddrs(); len(dialed) != 1 || dialed[0] != "db.internal.example:5432" {
		t.Errorf("qualified name dialed %v, want it resolved the way tsnet would", dialed)
	}
}

// The run log is the file people share for help. An endpoint URL can carry
// userinfo and a query, so the log gets the origin and nothing else.
func TestRedactURLKeepsOnlyTheOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://user:password@ai.example.ts.net:8443/v1?token=secret": "https://ai.example.ts.net:8443",
		"http://ai":        "http://ai",
		"not a url at all": "[redacted URL]",
	} {
		if got := redactURL(raw); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
