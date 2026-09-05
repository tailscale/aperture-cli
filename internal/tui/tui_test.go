package tui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
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
		Settings: config.Settings{Endpoints: []config.Endpoint{{URL: "http://ai"}}},
		LastLaunch: config.LaunchState{
			LastClientName:  "A",
			LastEndpointURL: "http://ai",
		},
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
		Settings: config.Settings{Endpoints: []config.Endpoint{{URL: "http://ai"}}},
		LastLaunch: config.LaunchState{
			LastClientName:  "A",
			LastEndpointURL: "http://ai",
		},
	}}
	root := m.rootMenu()
	for _, it := range root.Items {
		if !it.Hidden && strings.Contains(it.Label, "Quick select") {
			t.Errorf("unexpected quick-select row: %+v", it)
		}
	}
}

func TestRootMenu_NoQuickSelectWithoutRecordedEndpoint(t *testing.T) {
	fc := &fakeClient{
		name:       "A",
		installed:  true,
		replayCmd:  func() tea.Msg { return menu.ExecDoneMsg{} },
		quickLabel: "A via Whatever",
	}
	withFakeClients(t, []clients.Client{fc})

	m := &model{g: &config.Global{
		Settings:   config.Settings{Endpoints: []config.Endpoint{{URL: "http://ai"}}},
		LastLaunch: config.LaunchState{LastClientName: "A"},
	}}
	for _, it := range m.rootMenu().Items {
		if !it.Hidden && strings.Contains(it.Label, "Quick select") {
			t.Errorf("unexpected quick-select row without a recorded endpoint: %+v", it)
		}
	}
}

func TestQuickSelectRequiresRecordedEndpointToBeActive(t *testing.T) {
	replayed := false
	fc := &fakeClient{
		name:      "A",
		installed: true,
		replayCmd: func() tea.Msg {
			replayed = true
			return menu.ExecDoneMsg{}
		},
		quickLabel: "A via Whatever",
	}
	withFakeClients(t, []clients.Client{fc})

	active := config.Endpoint{URL: "http://ai", BridgeID: "bridge-current"}
	saved := config.Endpoint{URL: "http://ai", BridgeID: "bridge-saved"}
	m := &model{
		g: &config.Global{
			Settings:  config.Settings{Endpoints: []config.Endpoint{active, saved}},
			Providers: []config.ProviderInfo{{ID: "old-provider"}},
			LastLaunch: config.LaunchState{
				LastClientName:  "A",
				LastEndpointURL: saved.URL,
				LastBridgeID:    saved.BridgeID,
			},
		},
	}
	for _, item := range m.rootMenu().Items {
		if !item.Hidden && strings.Contains(item.Label, "Quick select") {
			t.Fatalf("quick-select shown for inactive endpoint: %+v", item)
		}
	}

	// Switching back to the recorded URL and bridge makes the saved launch
	// valid again without rewriting launch state.
	m.g.Settings.Endpoints = []config.Endpoint{saved, active}
	quick := m.rootMenu().Items[0]
	if !strings.Contains(quick.Label, "Quick select") || quick.Action().Cmd == nil {
		t.Fatalf("quick-select did not reappear for active recorded endpoint: %+v", quick)
	}
	quick.Action().Cmd()
	if !replayed {
		t.Fatal("quick-select did not replay saved launch")
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
	if got := m.menuHeader(m.top()); !strings.Contains(got, "timeout") {
		t.Errorf("failure header does not contain the underlying error: %q", got)
	}
}

func TestInstallFailureShowsError(t *testing.T) {
	m := &model{g: &config.Global{}, step: stepMenu}
	installMenu := &menu.Menu{Title: "Install Claude Code?"}
	m.resetStack(installMenu)

	m.Update(menu.InstallDoneMsg{Err: fmt.Errorf("exit status 1")})
	if m.step != stepError {
		t.Fatalf("step = %v, want stepError", m.step)
	}
	if !strings.Contains(m.errMsg, "Install failed: exit status 1") {
		t.Errorf("errMsg = %q, want install failure", m.errMsg)
	}
	if m.top() != installMenu {
		t.Error("install failure discarded the confirmation menu")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.step != stepMenu || m.top() != installMenu {
		t.Error("dismissing the error did not return to the install confirmation")
	}
}

func TestInstallCompletionRequiresDetectedBinary(t *testing.T) {
	client := &fakeClient{name: "Claude Code"}
	msg := installDoneMsg(client, false, nil)
	if msg.Err == nil || !strings.Contains(msg.Err.Error(), `"fake" was not found`) {
		t.Fatalf("installDoneMsg error = %v, want missing binary", msg.Err)
	}

	client.installed = true
	if msg := installDoneMsg(client, false, nil); msg.Err != nil {
		t.Fatalf("installDoneMsg error = %v for installed client", msg.Err)
	}

	wantErr := fmt.Errorf("installer failed")
	if msg := installDoneMsg(client, false, wantErr); msg.Err != wantErr {
		t.Fatalf("installDoneMsg error = %v, want original error %v", msg.Err, wantErr)
	}
}

func TestInstallCompletionCanSkipBinaryCheck(t *testing.T) {
	client := &fakeClient{name: "Claude Cowork"}
	if msg := installDoneMsg(client, true, nil); msg.Err != nil {
		t.Fatalf("installDoneMsg error = %v for user-driven install", msg.Err)
	}

	wantErr := fmt.Errorf("could not open download page")
	if msg := installDoneMsg(client, true, wantErr); msg.Err != wantErr {
		t.Fatalf("installDoneMsg error = %v, want original error %v", msg.Err, wantErr)
	}
}

func TestSetupGuideMenu_BridgeDoesNotRequireSystemTailscale(t *testing.T) {
	m := &model{g: &config.Global{
		ApertureHost: "http://aperture",
		Settings: config.Settings{
			Bridges:   []config.Bridge{{ID: "bridge-abcdef", Name: "Work Bridge"}},
			Endpoints: []config.Endpoint{{URL: "http://aperture", BridgeID: "bridge-abcdef"}},
		},
	}}

	guide := m.setupGuideMenu()
	if !strings.Contains(guide.Preamble, "embedded Tailscale node") {
		t.Errorf("bridge preamble = %q", guide.Preamble)
	}
	if !strings.Contains(guide.Preamble, "does not need Tailscale installed") {
		t.Errorf("bridge preamble gives system-Tailscale guidance: %q", guide.Preamble)
	}
}

func TestEndpointBridgeMenu_AddsFirstBridgeInline(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp+"/.config")
	m := &model{
		g: &config.Global{Settings: config.Settings{
			Endpoints: []config.Endpoint{{URL: "http://ai"}},
		}},
		step: stepMenu,
	}
	m.resetStack(m.endpointBridgeMenu())

	var add menu.MenuItem
	for _, item := range m.top().Items {
		if item.Label == "Add Bridge" {
			add = item
			break
		}
	}
	if add.Action == nil {
		t.Fatal("Add Bridge action not found")
	}
	add.Action()
	if m.step != stepInput || m.inputOnSave == nil {
		t.Fatal("Add Bridge did not prompt for a name")
	}
	if cmd := m.inputOnSave("Work Bridge"); cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("adding bridge returned %T: %v", msg, msg)
		}
	}

	if len(m.g.Settings.Bridges) != 1 || m.g.Settings.Bridges[0].Name != "Work Bridge" {
		t.Fatalf("bridges = %+v", m.g.Settings.Bridges)
	}
	if top := m.top(); top.Title != "Choose a bridge" || len(top.Items) != 2 || top.Items[0].Label != "Work Bridge" || top.Items[1].Label != "Add Bridge" {
		t.Fatalf("bridge chooser was not refreshed: %+v", top)
	}
	if m.step != stepInput || m.inputTitle != "Add Bridge Endpoint:" {
		t.Fatalf("adding bridge did not continue to endpoint URL: step=%v title=%q", m.step, m.inputTitle)
	}
}

func TestEndpointBridgeMenu_OffersAddBridgeWhenOneExists(t *testing.T) {
	m := &model{g: &config.Global{Settings: config.Settings{
		Bridges: []config.Bridge{{ID: "bridge-abcdef", Name: "First"}},
	}}}

	var labels []string
	for _, item := range m.endpointBridgeMenu().Items {
		if !item.Disabled {
			labels = append(labels, item.Label)
		}
	}
	if got := strings.Join(labels, ","); got != "First,Add Bridge" {
		t.Fatalf("bridge chooser labels = %q, want First,Add Bridge", got)
	}
}

func TestBridgeEndpointFailureKeepsPreviousEndpointActive(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp+"/.config")
	old := config.Endpoint{URL: "http://old"}
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Second"}
	m := &model{
		g: &config.Global{
			ApertureHost: "http://old",
			Settings: config.Settings{
				Bridges:   []config.Bridge{bridge},
				Endpoints: []config.Endpoint{old},
			},
			Providers: []config.ProviderInfo{{ID: "old-provider"}},
		},
		step:      stepMenu,
		connected: true,
	}
	chooser := m.endpointBridgeMenu()
	chooser.Items[0].Action()
	want := config.Endpoint{URL: "http://new", BridgeID: bridge.ID}
	cmd := m.inputOnSave(want.URL)
	if cmd == nil {
		t.Fatal("saving bridge endpoint did not begin activation")
	}
	if got := m.g.ActiveEndpoint(); !sameEndpoint(got, old) {
		t.Fatalf("active endpoint changed before activation: %+v", got)
	}
	if !m.endpointConfigured(want) {
		t.Fatalf("candidate endpoint was not saved: %+v", m.g.Settings.Endpoints)
	}

	msg := cmd()
	result, ok := msg.(endpointActivationResult)
	if !ok {
		t.Fatalf("activation message = %T", msg)
	}
	if !sameEndpoint(result.endpoint, want) || result.err == nil {
		t.Fatalf("activation result = %+v, want failed second bridge endpoint", result)
	}
	m.Update(result)
	if got := m.g.ActiveEndpoint(); !sameEndpoint(got, old) {
		t.Fatalf("failed activation changed active endpoint: %+v", got)
	}
	if m.g.ApertureHost != "http://old" || len(m.g.Providers) != 1 || m.g.Providers[0].ID != "old-provider" {
		t.Fatalf("failed activation replaced working runtime state: host=%q providers=%+v", m.g.ApertureHost, m.g.Providers)
	}
	if !strings.Contains(m.top().Preamble, "previous endpoint remains active") {
		t.Fatalf("failure menu does not explain retained endpoint: %q", m.top().Preamble)
	}
	var actions []string
	for _, item := range m.top().Items {
		actions = append(actions, item.Label)
	}
	for _, want := range []string{"Retry connection", "Edit endpoint URL", "Connection options", "Return to active endpoint", "Remove endpoint"} {
		if !slices.Contains(actions, want) {
			t.Errorf("failure actions = %v, missing %q", actions, want)
		}
	}
}

func TestDirectEndpointIsPromotedOnlyAfterModelsSucceed(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp+"/.config")
	srv := modelsServer(t)
	old := config.Endpoint{URL: "http://old"}
	m := &model{
		g: &config.Global{
			ApertureHost: "http://old",
			Settings:     config.Settings{Endpoints: []config.Endpoint{old}},
			Providers:    []config.ProviderInfo{{ID: "old-provider"}},
		},
		step: stepMenu,
	}

	m.addEndpointConnectionMenu().Items[0].Action()
	cmd := m.inputOnSave(srv.URL)
	if got := m.g.ActiveEndpoint(); !sameEndpoint(got, old) {
		t.Fatalf("active endpoint changed before /v1/models: %+v", got)
	}
	activation := cmd()
	m.Update(activation)
	if got := m.g.ActiveEndpoint(); got.URL != srv.URL {
		t.Fatalf("active endpoint = %+v, want %q", got, srv.URL)
	}
	if m.g.ApertureHost != srv.URL || len(m.g.Providers) != 1 || m.g.Providers[0].ID != "anthropic" {
		t.Fatalf("successful activation state: host=%q providers=%+v", m.g.ApertureHost, m.g.Providers)
	}
}

func TestBridgeLogSinkIgnoresLateLogsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan string, 1)
	logf := bridgeLogSink(ctx, ch)

	cancel()
	close(ch)
	// This is the sequence that panicked in v0.0.9: preflight had ended and
	// closed its channel, but tsnet emitted another background debug log.
	logf("late tsnet log")
}

func TestWaitBridgeLogDrainsBufferedLogBeforeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan string, 1)
	ch <- "final dial error"
	cancel()

	msg := waitBridgeLog(ctx, ch)()
	logMsg, ok := msg.(bridgeLogMsg)
	if !ok {
		t.Fatalf("message = %T, want bridgeLogMsg", msg)
	}
	if logMsg.line != "final dial error" {
		t.Errorf("line = %q, want final dial error", logMsg.line)
	}
}

func TestAppendBridgeLogRetainsDiagnosticsOverTsnetNoise(t *testing.T) {
	logs := []string{
		`Bridge network: state=Running tailnet="example.com" peers=598`,
		`Bridge target is visible: requested="aperture.example.ts.net"`,
	}
	for i := range bridgeLogLimit + 10 {
		logs = appendBridgeLog(logs, fmt.Sprintf("magicsock: noisy line %d", i))
	}
	logs = appendBridgeLog(logs, "Bridge dial failed: lookup failed")

	if len(logs) != bridgeLogLimit {
		t.Fatalf("len(logs) = %d, want %d", len(logs), bridgeLogLimit)
	}
	got := strings.Join(logs, "\n")
	for _, want := range []string{"Bridge network:", "Bridge target is visible:", "Bridge dial failed:"} {
		if !strings.Contains(got, want) {
			t.Errorf("logs lost %q:\n%s", want, got)
		}
	}
}

func TestFetchProvidersIncludesErrorResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bridge proxy error: lookup aperture", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := fetchProviders(srv.URL)
	if err == nil || !strings.Contains(err.Error(), "lookup aperture") {
		t.Fatalf("fetchProviders error = %v, want response detail", err)
	}
}

func TestFetchProvidersUsesModelsEndpoint(t *testing.T) {
	srv := modelsServerWithHandler(t, func(r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("User-Agent"); got != "aperture-cli" {
			t.Errorf("User-Agent = %q, want aperture-cli", got)
		}
	})
	defer srv.Close()

	got, err := fetchProviders(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "anthropic" || !got[0].SupportsEndpoint(config.EndpointAnthropicMessages) {
		t.Fatalf("fetchProviders() = %#v, want Anthropic Messages provider", got)
	}
}

func modelsServer(t *testing.T) *httptest.Server {
	t.Helper()
	return modelsServerWithHandler(t, nil)
}

func modelsServerWithHandler(t *testing.T, check func(*http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[{
				"id":"claude-opus-5",
				"supported_endpoints":["/v1/messages"],
				"metadata":{"provider":{
					"id":"anthropic","name":"Anthropic","description":"",
					"requires_client_auth":false,"upstream":"anthropic"
				}}
			}]
		}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWrapTextPreservesContentWithinTerminalWidth(t *testing.T) {
	m := &model{width: 40}
	text := "Bridge dial failed: address=aperture.example.ts.net:80 error=lookup aperture.example.ts.net on 127.0.0.53:53: no such host"
	got := m.wrapText("  ", text)

	for i, line := range strings.Split(got, "\n") {
		if width := ansi.StringWidth(line); width > m.width {
			t.Errorf("line %d width = %d, want <= %d: %q", i, width, m.width, line)
		}
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("line %d does not preserve indentation: %q", i, line)
		}
	}
	compact := func(s string) string { return strings.Join(strings.Fields(ansi.Strip(s)), "") }
	if compact(got) != compact(text) {
		t.Errorf("wrapped content changed:\n got: %q\nwant: %q", got, text)
	}
}

func TestFailureViewWrapsDiagnostics(t *testing.T) {
	m := &model{
		g: &config.Global{
			ApertureHost: "http://aperture.example.ts.net",
			Debug:        true,
			Settings: config.Settings{
				Bridges:   []config.Bridge{{ID: "bridge-abcdef", Name: "Work Bridge"}},
				Endpoints: []config.Endpoint{{URL: "http://aperture.example.ts.net", BridgeID: "bridge-abcdef"}},
			},
		},
		width:            50,
		forcedToEndpoint: true,
		preflightErr:     "bridge Work Bridge could not reach endpoint: lookup aperture.example.ts.net on 127.0.0.53:53: no such host",
		bridgeLogs: []string{
			`Bridge network: state=Running tailnet="example.com" dns_suffix="example.ts.net" peers=597`,
		},
	}
	m.resetStack(m.setupGuideMenu())

	for i, line := range strings.Split(m.View(), "\n") {
		if width := ansi.StringWidth(line); width > m.width {
			t.Errorf("line %d width = %d, want <= %d: %q", i, width, m.width, line)
		}
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

func TestRootHeaderShowsLogicalBridgeEndpoint(t *testing.T) {
	m := &model{
		g: &config.Global{
			ApertureHost: "http://127.0.0.1:41234",
			Settings: config.Settings{
				Bridges:   []config.Bridge{{ID: "bridge-abcdef", Name: "Work"}},
				Endpoints: []config.Endpoint{{URL: "http://ai", BridgeID: "bridge-abcdef"}},
			},
		},
		step:      stepMenu,
		connected: true,
	}
	m.resetStack(m.rootMenu())
	header := m.menuHeader(m.top())
	if !strings.Contains(header, "http://ai via Work") || strings.Contains(header, "127.0.0.1") {
		t.Fatalf("root header = %q", header)
	}
}
