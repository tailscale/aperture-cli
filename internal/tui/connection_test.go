package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
)

func TestEndpointEditPreservesVerifiedConnection(t *testing.T) {
	for _, outcome := range []string{"failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			m := pickerModel(t)
			withFakeClients(t, []clients.Client{})
			withFakeTailscale(t, tsConnected)
			old, host := m.g.ActiveEndpoint(), m.g.ApertureHost
			m.g.Providers = []config.ProviderInfo{{ID: "verified-provider"}}
			m.resetStack(m.rootMenu())
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}))
			defer srv.Close()
			m.promptEditEndpoint(old)
			cmd := m.inputOnSave(srv.URL)
			if outcome == "failure" {
				m.Update(activationResult(t, cmd))
			} else {
				m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				// The cancelled request can still deliver a success already queued.
				m.Update(endpointActivationResult{id: m.activationSeq, verified: bridges.Verified{Gateway: srv.URL}})
			}
			if got := m.g.ActiveEndpoint(); got != old {
				t.Errorf("%s replaced verified endpoint: got %+v, want %+v", outcome, got, old)
			}
			if m.g.ApertureHost != host || !m.connected || len(m.g.Providers) != 1 || m.g.Providers[0].ID != "verified-provider" {
				t.Errorf("%s changed verified runtime: host=%q connected=%v providers=%+v", outcome, m.g.ApertureHost, m.connected, m.g.Providers)
			}
			saved, err := config.LoadSettings()
			if err != nil {
				t.Fatal(err)
			}
			if saved.Endpoints[0] != old {
				t.Errorf("%s persisted unverified active endpoint: %+v", outcome, saved.Endpoints)
			}
		})
	}
}

func TestEndpointEditCommitsOnlyAfterSuccess(t *testing.T) {
	m := pickerModel(t)
	withFakeClients(t, []clients.Client{})
	old := m.g.ActiveEndpoint()
	srv := modelsServer(t)
	m.promptEditEndpoint(old)
	cmd := m.inputOnSave(srv.URL)
	if m.g.ActiveEndpoint() != old {
		t.Error("edit became active before verification")
	}
	m.Update(activationResult(t, cmd))
	if m.g.ActiveEndpoint().URL != srv.URL || m.g.ApertureHost != srv.URL || m.endpointConfigured(old) {
		t.Fatalf("verified edit did not replace original: %+v host=%q", m.g.Settings.Endpoints, m.g.ApertureHost)
	}
}

func TestTailnetSwitchInvalidatesSharedConnection(t *testing.T) {
	for _, shared := range []bool{true, false} {
		for _, outcome := range []string{"failure", "cancel", "remove"} {
			t.Run(fmt.Sprintf("shared=%v/%s", shared, outcome), func(t *testing.T) {
				m := pickerModel(t)
				withFakeClients(t, []clients.Client{&fakeClient{name: "Test agent", installed: true}})
				withFakeTailscale(t, tsConnected)
				target := m.g.Settings.Endpoints[1]
				if shared {
					m.g.Settings.Endpoints[0].BridgeID = target.BridgeID
					m.g.Settings.Endpoints[0].URL = "http://first"
				}
				m.g.ApertureHost = "http://127.0.0.1:12345"
				m.resetStack(m.rootMenu())
				m.connectVia(target, true)
				if outcome == "cancel" {
					m.Update(tea.KeyMsg{Type: tea.KeyEsc})
				} else {
					m.Update(endpointActivationResult{id: m.act.id, err: fmt.Errorf("model check failed after logout")})
					if outcome == "remove" {
						_, item := findItem(t, m.top().Items, "Remove endpoint")
						m.applyResult(item.Action())
					}
				}
				if m.connected == shared {
					t.Errorf("connected=%v after switch, want %v", m.connected, !shared)
				}
				if shared {
					if strings.Contains(m.top().Preamble, "previous endpoint remains active") {
						t.Error("offers a proxy closed by the tailnet switch")
					}
					// Even a route back to the root must not retain a launch action.
					root := m.rootMenu()
					for _, item := range root.Items {
						if item.Label == "Test agent" && !item.Disabled && item.Action != nil {
							t.Error("agent can launch through the closed proxy")
						}
					}
				}
			})
		}
	}
}

func TestEndpointEditSaveFailurePreservesRuntime(t *testing.T) {
	m := pickerModel(t)
	withFakeClients(t, []clients.Client{})
	withFakeTailscale(t, tsConnected)
	old, host := m.g.ActiveEndpoint(), m.g.ApertureHost
	srv := modelsServer(t)
	m.promptEditEndpoint(old)
	cmd := m.inputOnSave(srv.URL)
	before := m.g.Settings
	// Block the final atomic rename, on every supported platform.
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "aperture", "settings.json")
	if err := os.Rename(path, path+".before"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	m.Update(activationResult(t, cmd))
	if m.g.ActiveEndpoint() != old || m.g.ApertureHost != host || !reflect.DeepEqual(m.g.Settings, before) {
		t.Errorf("failed commit changed settings/runtime: %+v host=%q", m.g.Settings, m.g.ApertureHost)
	}
}

func TestEndpointEditRetryAndOverrideKeepOriginal(t *testing.T) {
	for _, action := range []string{"retry", "override", "edit again"} {
		t.Run(action, func(t *testing.T) {
			m := pickerModel(t)
			withFakeClients(t, []clients.Client{})
			withFakeTailscale(t, tsConnected)
			old := m.g.ActiveEndpoint()
			srv := modelsServer(t)
			m.promptEditEndpoint(old)
			m.inputOnSave(srv.URL)
			first := m.act.endpoint()
			m.Update(endpointActivationResult{id: m.act.id, err: fmt.Errorf("temporary failure")})
			var cmd tea.Cmd
			switch action {
			case "retry":
				_, item := findItem(t, m.top().Items, "Retry connection")
				cmd = item.Action().Cmd
			case "override":
				_, cmd = m.overrideActivationURL(srv.URL + "/new")
			case "edit again":
				m.promptEditEndpoint(first)
				cmd = m.inputOnSave(srv.URL + "/new")
			}
			m.Update(activationResult(t, cmd))
			if m.endpointConfigured(old) || !m.connected {
				t.Fatalf("%s lost pending replacement: %+v", action, m.g.Settings.Endpoints)
			}
			if action != "retry" && m.endpointConfigured(first) {
				t.Errorf("%s left the superseded candidate in settings", action)
			}
		})
	}
}

func TestEndpointEditSameCandidateCancellation(t *testing.T) {
	m := pickerModel(t)
	withFakeClients(t, []clients.Client{})
	withFakeTailscale(t, tsConnected)
	original := m.g.ActiveEndpoint()
	m.promptEditEndpoint(original)
	m.inputOnSave("http://candidate")
	candidate := m.act.endpoint()
	m.Update(endpointActivationResult{id: m.act.id, err: fmt.Errorf("temporary failure")})
	m.promptEditEndpoint(candidate)
	m.inputOnSave(candidate.URL)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.endpointConfigured(candidate) {
		t.Error("cancelling an unchanged edit retained its temporary candidate")
	}
	if m.g.ActiveEndpoint() != original || !m.connected {
		t.Errorf("cancellation changed the verified connection: %+v connected=%v", m.g.ActiveEndpoint(), m.connected)
	}
	saved, err := config.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range saved.Endpoints {
		if ep == candidate {
			t.Error("cancelled candidate remains on disk")
		}
	}
}
