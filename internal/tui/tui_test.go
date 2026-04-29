package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/profiles"
)

type quickTestProfile struct{}

func (quickTestProfile) Name() string       { return "Test Agent" }
func (quickTestProfile) BinaryName() string { return "test-agent" }
func (quickTestProfile) SupportedBackends() []profiles.Backend {
	return []profiles.Backend{
		{Type: profiles.BackendAnthropic, DisplayName: "Anthropic"},
	}
}
func (quickTestProfile) Env(string, profiles.Backend) (map[string]string, error) {
	return map[string]string{}, nil
}
func (quickTestProfile) RequiredCompat(profiles.Backend) []string {
	return []string{"anthropic_messages"}
}
func (quickTestProfile) ApplyModel(model string, env map[string]string) {
	env["MODEL"] = model
}

func TestRefreshLastSelectionResolvesProviderAndModel(t *testing.T) {
	p := quickTestProfile{}
	m := model{
		apertureHost:      "http://ai",
		state:             profiles.StateFile{LastProfileName: p.Name(), LastBackendType: string(profiles.BackendAnthropic), LastProviderID: "provider-one", LastModel: "provider-one/model-b"},
		manager:           profiles.NewManager(),
		installedProfiles: []profiles.Profile{p},
		providers: []profiles.ProviderInfo{
			{
				ID:            "provider-one",
				Name:          "Provider One",
				Models:        []string{"model-a", "model-b"},
				Compatibility: map[string]bool{"anthropic_messages": true},
			},
		},
		step: stepSelectProfile,
	}

	m.refreshLastSelection()

	if m.lastSelection == nil {
		t.Fatal("lastSelection is nil")
	}
	if got := m.lastSelection.provider.ID; got != "provider-one" {
		t.Fatalf("provider ID = %q, want %q", got, "provider-one")
	}
	if got := m.lastSelection.selectedModel; got != "provider-one/model-b" {
		t.Fatalf("selected model = %q, want %q", got, "provider-one/model-b")
	}

	view := m.View()
	if !strings.Contains(view, "[0] Quick select: Test Agent via Provider One - Anthropic - provider-one/model-b") {
		t.Fatalf("View() missing quick select row:\n%s", view)
	}
	if strings.Index(view, "[0] Quick select") > strings.Index(view, "[1] Test Agent") {
		t.Fatalf("quick select row should appear before profile options:\n%s", view)
	}

	m.resetProfileCursor()
	if m.profileCursor != -1 {
		t.Fatalf("profileCursor = %d, want -1 for quick select", m.profileCursor)
	}

	updated, _ := m.updateSelectProfile(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(model)
	if m.profileCursor != 0 {
		t.Fatalf("profileCursor after down = %d, want 0", m.profileCursor)
	}

	updated, _ = m.updateSelectProfile(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(model)
	if m.profileCursor != -1 {
		t.Fatalf("profileCursor after up = %d, want -1", m.profileCursor)
	}
}

func TestRefreshLastSelectionRejectsMissingModel(t *testing.T) {
	p := quickTestProfile{}
	m := model{
		state:             profiles.StateFile{LastProfileName: p.Name(), LastBackendType: string(profiles.BackendAnthropic), LastProviderID: "provider-one", LastModel: "provider-one/missing"},
		manager:           profiles.NewManager(),
		installedProfiles: []profiles.Profile{p},
		providers: []profiles.ProviderInfo{
			{
				ID:            "provider-one",
				Name:          "Provider One",
				Models:        []string{"model-a", "model-b"},
				Compatibility: map[string]bool{"anthropic_messages": true},
			},
		},
	}

	m.refreshLastSelection()

	if m.lastSelection != nil {
		t.Fatalf("lastSelection = %#v, want nil", m.lastSelection)
	}
}
