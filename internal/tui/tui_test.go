package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

// fakeClient is a minimal clients.Client for TUI tests.
type fakeClient struct {
	name        string
	installed   bool
	replayCmd   tea.Cmd
	quickLabel  string
	menuActions *menu.Menu // returned as Next from top-level action
}

func (c *fakeClient) Name() string          { return c.name }
func (c *fakeClient) BinaryName() string    { return "fake" }
func (c *fakeClient) CommonPaths() []string { return nil }
func (c *fakeClient) IsInstalled() bool     { return c.installed }
func (c *fakeClient) Install(*config.Global) clients.InstallPlan {
	return clients.InstallPlan{Hint: "install " + c.name}
}
func (c *fakeClient) Uninstall() clients.UninstallPlan {
	return clients.UninstallPlan{Hint: "uninstall " + c.name}
}
func (c *fakeClient) Menu(*config.Global) menu.MenuItem {
	return menu.MenuItem{
		Label:  c.name,
		Action: func() menu.Result { return menu.Result{Next: c.menuActions} },
	}
}
func (c *fakeClient) Replay(*config.Global) tea.Cmd          { return c.replayCmd }
func (c *fakeClient) QuickSelectLabel(*config.Global) string { return c.quickLabel }

// withFakeClients swaps the TUI's client registry for the duration of the test.
func withFakeClients(t *testing.T, cs []clients.Client) {
	t.Helper()
	orig := registeredClients
	registeredClients = func(*config.Global) []clients.Client { return cs }
	t.Cleanup(func() { registeredClients = orig })
}

func TestRootMenu_ShowsInstalledClients(t *testing.T) {
	withFakeClients(t, []clients.Client{
		&fakeClient{name: "A", installed: true},
		&fakeClient{name: "B", installed: false},
		&fakeClient{name: "C", installed: true},
	})

	m := &model{g: &config.Global{}}
	root := m.rootMenu()
	// Installed clients + hidden shortcut items (settings + install-agents).
	// Visible count: A, C (2). Plus a hidden Settings and hidden Install agents.
	visible := 0
	for _, it := range root.Items {
		if !it.Hidden {
			visible++
		}
	}
	if visible != 2 {
		t.Errorf("visible items = %d, want 2", visible)
	}
}

func TestRootMenu_QuickSelectPrepended(t *testing.T) {
	replayed := false
	fc := &fakeClient{
		name:       "A",
		installed:  true,
		replayCmd:  func() tea.Msg { replayed = true; return menu.ExecDoneMsg{} },
		quickLabel: "A via Whatever",
	}
	withFakeClients(t, []clients.Client{fc})

	m := &model{g: &config.Global{
		LastLaunch: config.LaunchState{LastClientName: "A"},
	}}
	root := m.rootMenu()

	// First visible item should be the quick-select row with Digit=0.
	var first menu.MenuItem
	for _, it := range root.Items {
		if !it.Hidden {
			first = it
			break
		}
	}
	if first.Digit != menu.DigitZero {
		t.Errorf("first visible Digit = %d, want DigitZero", first.Digit)
	}
	if !strings.Contains(first.Label, "Quick select") {
		t.Errorf("first visible Label = %q", first.Label)
	}

	// Invoking the action should run the replay cmd.
	res := first.Action()
	if res.Cmd == nil {
		t.Fatal("quick select action returned nil Cmd")
	}
	_ = res.Cmd() // run it
	if !replayed {
		t.Error("replay cmd was not invoked")
	}
}

func TestRootMenu_NoQuickSelectWhenReplayNil(t *testing.T) {
	fc := &fakeClient{name: "A", installed: true, replayCmd: nil}
	withFakeClients(t, []clients.Client{fc})

	m := &model{g: &config.Global{
		LastLaunch: config.LaunchState{LastClientName: "A"},
	}}
	root := m.rootMenu()
	for _, it := range root.Items {
		if !it.Hidden && strings.Contains(it.Label, "Quick select") {
			t.Errorf("unexpected quick-select row: %+v", it)
		}
	}
}

func TestMenuEngine_PushPop(t *testing.T) {
	sub := &menu.Menu{
		Title: "Sub",
		Items: []menu.MenuItem{
			{Label: "ok", Action: func() menu.Result { return menu.Result{Pop: true} }},
		},
	}
	fc := &fakeClient{name: "A", installed: true, menuActions: sub}
	withFakeClients(t, []clients.Client{fc})

	m := &model{g: &config.Global{}, step: stepMenu}
	m.resetStack(m.rootMenu())

	// Select the visible "A" item (first non-hidden).
	var idx int
	for i, it := range m.top().Items {
		if !it.Hidden {
			idx = i
			break
		}
	}
	mm, _ := m.activate(idx)
	m = mm.(*model)
	if m.top().Title != "Sub" {
		t.Fatalf("top after push = %q, want Sub", m.top().Title)
	}

	// Activate the Pop item.
	mm, _ = m.activate(0)
	m = mm.(*model)
	if m.top().Title != rootTitle {
		t.Fatalf("top after pop = %q, want %q", m.top().Title, rootTitle)
	}
}

func TestAssignTokens_RollsIntoLetters(t *testing.T) {
	var items []menu.MenuItem
	for i := 0; i < 15; i++ {
		items = append(items, menu.MenuItem{Label: "m", Action: func() menu.Result { return menu.Result{} }})
	}
	got := assignTokens(items)
	want := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "a", "b", "c", "d", "e", "f"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("token[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestAssignTokens_SkipsReservedShortcuts(t *testing.T) {
	items := []menu.MenuItem{
		{Label: "normal", Action: func() menu.Result { return menu.Result{} }},
		{Label: "normal", Action: func() menu.Result { return menu.Result{} }},
		{Label: "hidden", Shortcut: "d", Hidden: true, Action: func() menu.Result { return menu.Result{} }},
	}
	got := assignTokens(items)
	// Hidden item gets no token.
	if got[2] != "" {
		t.Errorf("hidden token = %q, want empty", got[2])
	}
	// Auto tokens must not include "d".
	for i := 0; i < 2; i++ {
		if got[i] == "d" {
			t.Errorf("auto token[%d] = %q, should skip reserved 'd'", i, got[i])
		}
	}
}

func TestAssignTokens_DigitZeroPinned(t *testing.T) {
	items := []menu.MenuItem{
		{Label: "quick", Digit: menu.DigitZero, Action: func() menu.Result { return menu.Result{} }},
		{Label: "a", Action: func() menu.Result { return menu.Result{} }},
	}
	got := assignTokens(items)
	if got[0] != "0" {
		t.Errorf("pinned token = %q, want 0", got[0])
	}
	if got[1] != "1" {
		t.Errorf("first auto = %q, want 1", got[1])
	}
}

func TestSettingsMenu_ToggleYolo(t *testing.T) {
	withFakeClients(t, nil)
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp+"/.config")

	g := &config.Global{}
	m := &model{g: g, step: stepMenu}
	m.resetStack(m.settingsMenu())

	idx := -1
	for i, it := range m.top().Items {
		if strings.HasPrefix(it.Label, "YOLO mode:") {
			idx = i
			break
		}
	}
	if idx == -1 {
		t.Fatal("YOLO item not found")
	}
	res := m.top().Items[idx].Action()
	if !g.Settings.YoloMode {
		t.Error("YoloMode = false after toggle")
	}
	if res.Replace == nil {
		t.Fatal("toggle should replace menu in place")
	}
	if !strings.Contains(res.Replace.Items[idx].Label, "YOLO mode: on") {
		t.Errorf("new label = %q", res.Replace.Items[idx].Label)
	}
}

func TestSettingsMenu_BridgesFirst(t *testing.T) {
	m := &model{g: &config.Global{}, step: stepMenu}
	menu := m.settingsMenu()
	if len(menu.Items) == 0 || menu.Items[0].Label != "Bridges" {
		t.Fatalf("first settings item = %+v, want Bridges", menu.Items)
	}
}

// withFakeTailscale overrides checkTailscale for the duration of a test.
func withFakeTailscale(t *testing.T, status tailscaleStatus) {
	t.Helper()
	orig := checkTailscale
	checkTailscale = func() tailscaleStatus { return status }
	t.Cleanup(func() { checkTailscale = orig })
}

func TestSetupGuideMenu_TailscaleNotInstalled(t *testing.T) {
	withFakeTailscale(t, tsNotInstalled)
	m := &model{g: &config.Global{ApertureHost: "http://ai"}}
	guide := m.setupGuideMenu()

	if guide.Title != setupGuideTitle {
		t.Errorf("title = %q, want %q", guide.Title, setupGuideTitle)
	}
	if !strings.Contains(guide.Preamble, "tailscale.com/download") {
		t.Error("preamble missing Tailscale download URL")
	}
	actionCount := 0
	for _, it := range guide.Items {
		if it.Action != nil {
			actionCount++
		}
	}
	if actionCount != 3 {
		t.Errorf("actionable items = %d, want 3", actionCount)
	}
}

func TestSetupGuideMenu_TailscaleConnected(t *testing.T) {
	withFakeTailscale(t, tsConnected)
	m := &model{g: &config.Global{ApertureHost: "http://ai"}}
	guide := m.setupGuideMenu()

	if !strings.Contains(guide.Preamble, "aperture.tailscale.com") {
		t.Error("preamble missing Aperture provisioning URL")
	}
	if !strings.Contains(guide.Preamble, "Tailscale is connected") {
		t.Error("preamble missing 'Tailscale is connected' message")
	}
}

func TestSetupGuideMenu_RetryAction(t *testing.T) {
	withFakeTailscale(t, tsConnected)
	m := &model{g: &config.Global{ApertureHost: "http://ai"}}
	guide := m.setupGuideMenu()

	for _, it := range guide.Items {
		if it.Label == "Retry connection" {
			res := it.Action()
			if res.Cmd == nil {
				t.Error("Retry action returned nil Cmd")
			}
			return
		}
	}
	t.Error("Retry connection item not found")
}

func TestSetupGuideMenu_ConnectionOptionsAction(t *testing.T) {
	withFakeTailscale(t, tsConnected)
	m := &model{g: &config.Global{ApertureHost: "http://ai"}}
	guide := m.setupGuideMenu()

	for _, it := range guide.Items {
		if it.Label == "Connection options" {
			res := it.Action()
			if res.Next == nil || res.Next.Title != endpointsTitle {
				t.Errorf("Connection options should push endpoints menu, got %+v", res.Next)
			}
			return
		}
	}
	t.Error("Connection options item not found")
}

func TestPreflightFailure_ShowsSetupGuide(t *testing.T) {
	withFakeTailscale(t, tsNotInstalled)
	withFakeClients(t, nil)
	m := &model{
		g:    &config.Global{ApertureHost: "http://ai"},
		step: stepPreflight,
	}
	m.Update(preflightResult{err: fmt.Errorf("connection refused")})
	if !m.forcedToEndpoint {
		t.Error("forcedToEndpoint should be true")
	}
	if m.top() == nil || m.top().Title != setupGuideTitle {
		title := ""
		if m.top() != nil {
			title = m.top().Title
		}
		t.Errorf("top menu title = %q, want %q", title, setupGuideTitle)
	}
}

func TestEndpointActivationFailure_ShowsSetupGuide(t *testing.T) {
	withFakeTailscale(t, tsConnected)
	withFakeClients(t, nil)
	m := &model{
		g: &config.Global{ApertureHost: "http://ai"},
	}
	m.Update(endpointActivationResult{
		endpoint: config.Endpoint{URL: "http://ai"},
		err:      fmt.Errorf("timeout"),
	})
	if !m.forcedToEndpoint {
		t.Error("forcedToEndpoint should be true")
	}
	if m.top() == nil || m.top().Title != setupGuideTitle {
		title := ""
		if m.top() != nil {
			title = m.top().Title
		}
		t.Errorf("top menu title = %q, want %q", title, setupGuideTitle)
	}
}

func TestEndpointLabel_ShowsBridge(t *testing.T) {
	m := &model{g: &config.Global{
		Settings: config.Settings{
			Bridges: []config.Bridge{{ID: "bridge-abcdef", Name: "Work"}},
		},
	}}
	got := m.endpointLabel(config.Endpoint{URL: "http://ai", BridgeID: "bridge-abcdef"})
	if got != "http://ai via Work" {
		t.Errorf("endpointLabel = %q", got)
	}
}
