// Package tui is the bubbletea-driven interactive launcher. It renders a
// generic navigable menu stack described by internal/menu; each entry on
// the stack comes from either the root client picker (built from
// internal/clients) or a sub-menu pushed by a client's action closure.
// The TUI owns only the preflight HTTP check, a single-line text input
// step, and error screens — everything else is expressed as Menu values.
package tui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

type step int

const (
	stepPreflight step = iota
	stepMenu           // rendering the top of the stack
	stepInput          // single-line text input (add-endpoint)
	stepError          // fatal/fixable error message
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).MarginBottom(1)
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	dimStyle      = lipgloss.NewStyle().Faint(true)
	greenStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))

	dotYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("●")
	dotGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("●")
	dotRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("●")
)

const (
	providerFetchTimeout       = 10 * time.Second
	bridgeProviderFetchTimeout = 30 * time.Second
)

// NewModel returns the TUI model. g holds the persisted launcher state
// (settings, endpoints, last launch). buildVersion is shown at the bottom
// of the client picker.
func NewModel(g *config.Global, buildVersion string, bridgeManager *bridges.Manager) tea.Model {
	return &model{
		g:             g,
		buildVersion:  buildVersion,
		bridgeManager: bridgeManager,
		step:          stepPreflight,
	}
}

type model struct {
	g             *config.Global
	buildVersion  string
	bridgeManager *bridges.Manager

	step step

	// Terminal dimensions, refreshed on tea.WindowSizeMsg. Zero until the
	// first message arrives.
	width, height int

	// Menu stack. The top (last element) is what's rendered and receives key
	// input during stepMenu.
	stack []*menu.Menu
	// Per-menu cursor positions, one per stack entry.
	cursors []int

	// Input step state.
	inputTitle  string
	inputPrompt string
	input       textField
	inputOnSave func(value string) tea.Cmd

	// Error screen state.
	errMsg string

	// Preflight state.
	act              *activation
	activationSeq    int
	preflightErr     string
	forcedToEndpoint bool // true when preflight failure dropped user on endpoints menu
	bridgeLogs       []string
	failedEndpoint   *config.Endpoint
	connected        bool
}

// activation is the connection attempt currently on screen. It owns the
// attempt's identity and cancellation handle, and the URL the user can type
// over the top of it while it runs; the log tail it produces stays on the
// model because the failure screen still renders it after the attempt ends.
//
// cancel is nil for attempts that cannot be interrupted (the post-launch
// re-check), which is what makes Esc and the inline override inert there.
type activation struct {
	id       int
	endpoint config.Endpoint
	label    string
	cancel   context.CancelFunc
	// ephemeral records that this flow is what put endpoint into settings,
	// so abandoning or overriding the attempt takes it back out instead of
	// leaving an endpoint nobody chose.
	ephemeral bool
	logCh     chan string
	logCtx    context.Context
	// authURL is the Tailscale login link already surfaced for this attempt.
	// tsnet reprints its line every few seconds, so this is what keeps the
	// log tail from filling with one repeated URL and the browser from being
	// opened again on each repeat.
	authURL string
	// override is the inline "different Aperture URL" editor shown while a
	// bridge attempt runs.
	override textField
}

// cancelable reports whether Esc can interrupt this attempt.
func (a *activation) cancelable() bool { return a != nil && a.cancel != nil }

// overridable reports whether the attempt accepts a typed URL in place of the
// one being probed. Only bridge attempts start from a guessed URL.
func (a *activation) overridable() bool { return a.cancelable() && a.endpoint.BridgeID != "" }

// textField is the shared single-line editor behind the add-endpoint input
// step and the inline URL override on the connect screen.
type textField struct {
	value string
	err   string
}

// insert appends the text a key press carries. A typed character arrives as
// one rune and a pasted URL as many in a single message; both are text, and
// dropping the paste would leave the user retyping an endpoint by hand. Named
// keys and Alt chords carry no text and are ignored, as are control runes:
// matching on the key's String() would append "up" when someone presses Up.
func (f *textField) insert(msg tea.KeyMsg) {
	if msg.Alt || (msg.Type != tea.KeyRunes && msg.Type != tea.KeySpace) {
		return
	}
	if len(msg.Runes) == 0 {
		return
	}
	for _, r := range msg.Runes {
		if unicode.IsControl(r) {
			return
		}
	}
	f.value += string(msg.Runes)
	f.err = ""
}

func (f *textField) backspace() {
	if f.value == "" {
		return
	}
	_, size := utf8.DecodeLastRuneInString(f.value)
	f.value = f.value[:len(f.value)-size]
	f.err = ""
}

func (f *textField) reset() { *f = textField{} }

func (m *model) Init() tea.Cmd {
	return m.activateEndpointCmd(m.g.ActiveEndpoint())
}

// preflightResult is emitted when the /v1/models check completes.
type preflightResult struct {
	host      string
	providers []config.ProviderInfo
	err       error
}

type endpointActivationResult struct {
	// id identifies the attempt this result belongs to. A result whose id no
	// longer matches the current attempt is stale: the user cancelled it or
	// typed a different URL over it, and its outcome must not be applied.
	id        int
	endpoint  config.Endpoint
	host      string
	providers []config.ProviderInfo
	err       error
}

type bridgeLogMsg struct {
	ch   chan string
	line string
}
type bridgeLogDoneMsg struct{ ch chan string }

// browserOpenMsg reports whether the desktop opener for a bridge login link
// started. id ties it to the attempt that asked, so a cancelled attempt's
// failure does not print over the next one.
type browserOpenMsg struct {
	id  int
	err error
}

func openURLCmd(id int, url string) tea.Cmd {
	return func() tea.Msg { return browserOpenMsg{id: id, err: openURL(url)} }
}

type quitMsg struct{ Err error }

func runPreflight(host string) tea.Cmd {
	return func() tea.Msg {
		provs, err := fetchProviders(host)
		return preflightResult{host: host, providers: provs, err: err}
	}
}

func fetchProviders(host string) ([]config.ProviderInfo, error) {
	return fetchProvidersContext(context.Background(), host, providerFetchTimeout)
}

func fetchProvidersContext(ctx context.Context, host string, timeout time.Duration) ([]config.ProviderInfo, error) {
	client := &http.Client{Timeout: timeout}
	url := strings.TrimRight(host, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Aperture intentionally filters model results for Claude Code user agents.
	// Discovery needs the full grant-filtered model list for every harness.
	req.Header.Set("User-Agent", "aperture-cli")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return nil, fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, url, detail)
		}
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	provs, err := config.ParseProviders(body)
	if err != nil {
		return nil, fmt.Errorf("could not parse models response: %w", err)
	}
	return provs, nil
}

func (m *model) activateEndpointCmd(ep config.Endpoint) tea.Cmd {
	return m.activateEndpoint(ep, false, false)
}

// activateEndpoint starts a cancellable attempt to connect to ep. ephemeral
// marks an endpoint this flow just wrote to settings on the user's behalf, so
// cancelling or overriding the attempt can take it back out again.
// switchTailnet logs the bridge out before connecting, so the attempt starts
// from a login prompt rather than the tailnet the node is already on.
func (m *model) activateEndpoint(ep config.Endpoint, ephemeral, switchTailnet bool) tea.Cmd {
	m.stopActivation()
	m.step = stepPreflight
	m.preflightErr = ""
	m.bridgeLogs = nil

	ctx, cancel := context.WithCancel(context.Background())
	m.activationSeq++
	act := &activation{
		id:        m.activationSeq,
		endpoint:  ep,
		label:     "Checking " + ep.URL + " ...",
		cancel:    cancel,
		ephemeral: ephemeral,
	}
	m.act = act

	bridge, ok := m.g.Bridge(ep.BridgeID)
	switch {
	case ep.BridgeID == "":
		return func() tea.Msg {
			defer cancel()
			provs, err := fetchProvidersContext(ctx, ep.URL, providerFetchTimeout)
			return endpointActivationResult{id: act.id, endpoint: ep, host: ep.URL, providers: provs, err: err}
		}
	case !ok:
		return func() tea.Msg {
			defer cancel()
			return endpointActivationResult{
				id:       act.id,
				endpoint: ep,
				host:     ep.URL,
				err:      fmt.Errorf("bridge %s is not configured", ep.BridgeID),
			}
		}
	case m.bridgeManager == nil:
		return func() tea.Msg {
			defer cancel()
			return endpointActivationResult{
				id:       act.id,
				endpoint: ep,
				host:     ep.URL,
				err:      fmt.Errorf("bridge manager is not configured"),
			}
		}
	}

	ch := make(chan string, 32)
	act.logCh = ch
	act.logCtx = ctx
	act.label = "Connecting bridge " + bridge.Name + " to " + ep.URL + " ..."
	if switchTailnet {
		act.label = "Switching bridge " + bridge.Name + " to a different tailnet ..."
	}
	bridgeLogf := bridgeLogSink(ctx, ch)
	activate := func() tea.Msg {
		defer cancel()
		// Inside the attempt, so it shares the attempt's cancellation and log
		// sink: the new login link is what the user needs on screen, and Esc
		// has to reach a logout that stalls on the old tailnet.
		if switchTailnet {
			if err := m.bridgeManager.SwitchTailnet(ctx, bridge, bridgeLogf); err != nil {
				return endpointActivationResult{id: act.id, endpoint: ep, host: ep.URL, err: err}
			}
		}
		localURL, err := m.bridgeManager.Activate(ctx, bridge, ep.URL, bridgeLogf)
		if err != nil {
			return endpointActivationResult{id: act.id, endpoint: ep, host: ep.URL, err: err}
		}
		provs, err := fetchProvidersContext(ctx, localURL, bridgeProviderFetchTimeout)
		if err != nil {
			err = fmt.Errorf("bridge %s could not reach %s: %w", bridge.Name, ep.URL, err)
		}
		return endpointActivationResult{id: act.id, endpoint: ep, host: localURL, providers: provs, err: err}
	}
	return tea.Batch(activate, waitBridgeLog(ctx, ch))
}

// recordBridgeTailnet saves the tailnet a bridge just connected through, so
// the connection picker can name it on a later run before the bridge is
// started again. A failed write is not worth interrupting a connection that
// worked: the picker falls back to saying the tailnet is not known yet.
func (m *model) recordBridgeTailnet(ep config.Endpoint) {
	if ep.BridgeID == "" {
		return
	}
	name := m.bridgeManager.Tailnet(ep.BridgeID)
	if name == "" {
		return
	}
	_ = m.g.SetBridgeTailnet(ep.BridgeID, name)
}

// stopActivation ends the in-flight attempt without touching settings. The
// attempt's own goroutine still delivers a result; the id check in Update
// discards it.
func (m *model) stopActivation() {
	act := m.act
	if act == nil {
		return
	}
	if act.cancel != nil {
		act.cancel()
		act.cancel = nil
	}
	act.logCh = nil
	act.logCtx = nil
}

// discardActivation stops the in-flight attempt and removes the endpoint this
// flow added for it, so an abandoned connection leaves nothing behind.
func (m *model) discardActivation() error {
	act := m.act
	if act == nil {
		return nil
	}
	m.stopActivation()
	if !act.ephemeral {
		return nil
	}
	act.ephemeral = false
	return m.removeEndpoint(act.endpoint)
}

// removeEndpoint deletes ep from settings. The active endpoint at index 0 is
// left alone: it is the connection the user falls back to.
func (m *model) removeEndpoint(ep config.Endpoint) error {
	for i, existing := range m.g.Settings.Endpoints {
		if i == 0 || !sameEndpoint(existing, ep) {
			continue
		}
		return m.g.RemoveEndpoint(i)
	}
	return nil
}

// cancelActivation abandons the attempt on screen and returns to the menu the
// user started it from. At startup there is no such menu, so the setup guide
// takes its place.
func (m *model) cancelActivation() (tea.Model, tea.Cmd) {
	act := m.act
	if act == nil {
		return m, nil
	}
	endpoint := act.endpoint
	if err := m.discardActivation(); err != nil {
		m.errMsg = "could not remove endpoint: " + err.Error()
		m.step = stepError
		return m, nil
	}
	m.act = nil
	m.step = stepMenu
	if len(m.stack) == 0 {
		m.preflightErr = "connection cancelled"
		m.forcedToEndpoint = true
		m.failedEndpoint = &endpoint
		m.resetStack(m.setupGuideMenu())
	}
	return m, tea.ClearScreen
}

// overrideActivationURL swaps the URL being probed for one the user typed,
// without waiting for the guess to time out.
func (m *model) overrideActivationURL(value string) (tea.Model, tea.Cmd) {
	act := m.act
	if act == nil {
		return m, nil
	}
	next, err := config.ParseEndpoint(value, act.endpoint.BridgeID)
	if err != nil {
		// Keep the running attempt: the typo costs nothing, and the guess
		// may still land while the user fixes it.
		act.override.err = err.Error()
		return m, nil
	}
	if sameEndpoint(next, act.endpoint) {
		act.override.reset()
		return m, nil
	}
	m.stopActivation()

	ephemeral := !m.endpointConfigured(next)
	if act.ephemeral {
		// Replace rather than add: the guessed endpoint was never reachable
		// and nobody asked for it.
		if err := m.g.ReplaceEndpoint(act.endpoint, next); err != nil {
			m.errMsg = err.Error()
			m.step = stepError
			return m, nil
		}
	} else if ephemeral {
		if err := m.g.UpsertEndpoint(next); err != nil {
			m.errMsg = err.Error()
			m.step = stepError
			return m, nil
		}
	}
	return m, m.activateEndpoint(next, ephemeral, false)
}

func bridgeLogSink(ctx context.Context, ch chan<- string) func(string) {
	return func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		select {
		case <-ctx.Done():
		case ch <- line:
		default:
		}
	}
}

func waitBridgeLog(ctx context.Context, ch chan string) tea.Cmd {
	return func() tea.Msg {
		// Drain anything already logged before observing cancellation. This
		// preserves the final dial/proxy error when preflight cancels the log
		// context immediately after the request returns.
		select {
		case line := <-ch:
			return bridgeLogMsg{ch: ch, line: line}
		default:
		}
		select {
		case line := <-ch:
			return bridgeLogMsg{ch: ch, line: line}
		case <-ctx.Done():
			return bridgeLogDoneMsg{ch: ch}
		}
	}
}

func (m *model) quitCmd() tea.Cmd {
	var cancel context.CancelFunc
	if m.act != nil {
		cancel = m.act.cancel
	}
	bridgeManager := m.bridgeManager
	return func() tea.Msg {
		if cancel != nil {
			cancel()
		}
		if bridgeManager == nil {
			return quitMsg{}
		}
		return quitMsg{Err: bridgeManager.Close()}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case preflightResult:
		if msg.err != nil {
			m.connected = false
			m.preflightErr = msg.err.Error()
			m.forcedToEndpoint = true
			failed := m.g.ActiveEndpoint()
			m.failedEndpoint = &failed
			m.step = stepMenu
			m.resetStack(m.setupGuideMenu())
			return m, nil
		}
		m.g.Providers = msg.providers
		m.connected = true
		m.preflightErr = ""
		m.forcedToEndpoint = false
		m.failedEndpoint = nil
		m.step = stepMenu
		m.resetStack(m.rootMenu())
		return m, tea.ClearScreen

	case endpointActivationResult:
		if m.act == nil || msg.id != m.act.id {
			// Cancelled or overridden: a newer attempt owns the screen.
			return m, nil
		}
		m.act.cancel = nil
		if msg.err != nil {
			if sameEndpoint(msg.endpoint, m.g.ActiveEndpoint()) {
				m.connected = false
			}
			m.preflightErr = msg.err.Error()
			m.forcedToEndpoint = true
			failed := msg.endpoint
			m.failedEndpoint = &failed
			m.step = stepMenu
			m.resetStack(m.setupGuideMenu())
			return m, nil
		}
		if !sameEndpoint(m.g.ActiveEndpoint(), msg.endpoint) {
			if err := m.g.SetActiveEndpoint(msg.endpoint); err != nil {
				m.preflightErr = "could not save active endpoint: " + err.Error()
				m.forcedToEndpoint = true
				failed := msg.endpoint
				m.failedEndpoint = &failed
				m.step = stepMenu
				m.resetStack(m.setupGuideMenu())
				return m, nil
			}
		}
		m.recordBridgeTailnet(msg.endpoint)
		m.g.ApertureHost = msg.host
		m.g.Providers = msg.providers
		m.connected = true
		m.preflightErr = ""
		m.forcedToEndpoint = false
		m.failedEndpoint = nil
		m.step = stepMenu
		m.resetStack(m.rootMenu())
		return m, tea.ClearScreen

	case bridgeLogMsg:
		if m.act == nil || m.act.logCh != msg.ch {
			return m, nil
		}
		next := waitBridgeLog(m.act.logCtx, m.act.logCh)
		if url := authURLFromLog(msg.line); url != "" {
			if url == m.act.authURL {
				return m, next // tsnet reprinting the same link
			}
			m.act.authURL = url
			m.bridgeLogs = appendBridgeLog(m.bridgeLogs, bridgeAuthLogPrefix+url)
			return m, tea.Batch(next, openURLCmd(m.act.id, url))
		}
		m.bridgeLogs = appendBridgeLog(m.bridgeLogs, msg.line)
		return m, next

	case browserOpenMsg:
		// Only the failure is worth a line: a browser that opened is on the
		// user's screen, and the link itself is already in the log tail.
		if m.act == nil || m.act.id != msg.id || msg.err == nil {
			return m, nil
		}
		m.bridgeLogs = appendBridgeLog(m.bridgeLogs, "Could not open a browser here ("+msg.err.Error()+"). Open the link above to authorize.")
		return m, nil

	case bridgeLogDoneMsg:
		if m.act != nil && m.act.logCh == msg.ch {
			m.act.logCh = nil
			m.act.logCtx = nil
		}
		return m, nil

	case quitMsg:
		if msg.Err != nil {
			m.errMsg = "Error shutting down bridges: " + msg.Err.Error()
			m.step = stepError
			return m, nil
		}
		return m, tea.Quit

	case menu.ExecDoneMsg:
		// A client's foreground launch has exited. Re-run preflight: the
		// user may have changed things outside the launcher while the
		// agent was running.
		m.popToRoot()
		m.step = stepPreflight
		// No cancel handle: this re-check owns the screen until it answers.
		m.act = &activation{label: "Checking " + m.g.ApertureHost + " ..."}
		return m, runPreflight(m.g.ApertureHost)

	case menu.InstallDoneMsg:
		if msg.Err != nil {
			m.errMsg = "Install failed: " + msg.Err.Error()
			m.step = stepError
			return m, nil
		}
		// Rebuild the root menu so install state is reflected.
		m.step = stepMenu
		m.resetStack(m.rootMenu())
		return m, tea.ClearScreen

	case menu.LaunchDoneMsg:
		// Desktop-style launch returned immediately; stay on root menu.
		m.popToRoot()
		m.step = stepMenu
		m.resetStack(m.rootMenu())
		return m, tea.ClearScreen

	case menu.SimpleDoneMsg:
		if msg.Err != nil {
			m.errMsg = msg.Err.Error()
			m.step = stepError
			return m, nil
		}
		m.popOne()
		return m, nil

	case tea.KeyMsg:
		switch m.step {
		case stepPreflight:
			return m.updatePreflight(msg)
		case stepError:
			switch msg.String() {
			case "ctrl+c", "q":
				return m, m.quitCmd()
			default:
				m.step = stepMenu
				return m, nil
			}
		case stepInput:
			return m.updateInput(msg)
		case stepMenu:
			return m.updateMenu(msg)
		}
	}
	return m, nil
}

const bridgeLogLimit = 12

// appendBridgeLog bounds the on-screen bridge log while retaining the
// diagnostics produced by aperture-cli itself. Verbose tsnet messages can be
// frequent enough to otherwise evict the network identity, target visibility,
// and dial failure that -debug is intended to expose.
func appendBridgeLog(logs []string, line string) []string {
	logs = append(logs, line)
	for len(logs) > bridgeLogLimit {
		drop := 0
		for i, line := range logs {
			if !importantBridgeLog(line) {
				drop = i
				break
			}
		}
		logs = append(logs[:drop], logs[drop+1:]...)
	}
	return logs
}

// bridgeAuthLogPrefix labels the login link on the connect screen. It is also
// an importantBridgeLog prefix: the link is the one line the user must act on,
// and tsnet's own chatter would otherwise push it off the tail.
const bridgeAuthLogPrefix = "Authorize this bridge in your browser: "

func importantBridgeLog(line string) bool {
	for _, prefix := range []string{
		bridgeAuthLogPrefix,
		"Could not open a browser here",
		"Bridge network:",
		"Bridge health:",
		"Bridge target ",
		"Bridge dial failed:",
		"Bridge proxy error:",
		"Could not read bridge network status:",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func (m *model) updateMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	top := m.top()
	if top == nil {
		return m, nil
	}
	cursor := m.cursor()

	switch msg.String() {
	case "ctrl+c":
		return m, m.quitCmd()

	case "q":
		// "q" quits from the root only; on sub-menus it pops.
		if len(m.stack) <= 1 {
			return m, m.quitCmd()
		}
		m.popOne()
		return m, tea.ClearScreen

	case "esc":
		if top.OnBack != nil {
			if cmd := top.OnBack(); cmd != nil {
				return m, cmd
			}
			return m, nil
		}
		if len(m.stack) <= 1 {
			// Root menu ignores Esc.
			return m, nil
		}
		m.popOne()
		return m, tea.ClearScreen

	case "up", "k":
		visible, _, _ := m.menuLayout(top)
		if p := visiblePos(visible, cursor); p > 0 {
			m.setCursor(visible[p-1])
		}
		return m, nil

	case "down", "j":
		visible, _, _ := m.menuLayout(top)
		if p := visiblePos(visible, cursor); p >= 0 && p < len(visible)-1 {
			m.setCursor(visible[p+1])
		}
		return m, nil

	case "left", "h":
		visible, twoCols, half := m.menuLayout(top)
		if !twoCols {
			return m, nil
		}
		if p := visiblePos(visible, cursor); p >= half {
			m.setCursor(visible[p-half])
		}
		return m, nil

	case "right", "l":
		visible, twoCols, half := m.menuLayout(top)
		if !twoCols {
			return m, nil
		}
		if p := visiblePos(visible, cursor); p >= 0 && p < half && p+half < len(visible) {
			m.setCursor(visible[p+half])
		}
		return m, nil

	case "enter":
		return m.activate(cursor)

	default:
		s := msg.String()
		if len(s) != 1 {
			return m, nil
		}
		// Single-char shortcut (explicit Shortcut wins over auto-assigned
		// tokens so e.g. "d" on the endpoints menu always deletes).
		// Hidden items are allowed: the root menu registers Settings and
		// Install-agents as hidden Shortcut-only rows.
		for i, it := range top.Items {
			if it.Disabled {
				continue
			}
			if it.Shortcut != "" && it.Shortcut == s {
				return m.activate(i)
			}
		}
		// Auto-assigned or explicit-Digit token.
		tokens := assignTokens(top.Items)
		for i, tok := range tokens {
			if tok != "" && tok == s {
				return m.activate(i)
			}
		}
	}
	return m, nil
}

func (m *model) activate(idx int) (tea.Model, tea.Cmd) {
	top := m.top()
	if top == nil || idx < 0 || idx >= len(top.Items) {
		return m, nil
	}
	item := top.Items[idx]
	if item.Disabled || item.Action == nil {
		return m, nil
	}
	// Only move the cursor onto visible rows. Hidden shortcut handlers
	// (e.g. endpoints menu's "d" delete) read m.cursor() to know which
	// visible row to act on — moving the cursor onto the hidden handler
	// itself would strand it off-screen and break subsequent actions.
	if !item.Hidden {
		m.setCursor(idx)
	}
	res := item.Action()
	return m.applyResult(res)
}

func (m *model) applyResult(res menu.Result) (tea.Model, tea.Cmd) {
	switch {
	case res.Quit:
		return m, m.quitCmd()
	case res.Pop:
		m.popOne()
		return m, tea.ClearScreen
	case res.Replace != nil:
		if len(m.stack) > 0 {
			m.stack[len(m.stack)-1] = res.Replace
			m.cursors[len(m.cursors)-1] = 0
		} else {
			m.stack = append(m.stack, res.Replace)
			m.cursors = append(m.cursors, 0)
		}
		return m, tea.ClearScreen
	case res.Next != nil:
		m.stack = append(m.stack, res.Next)
		m.cursors = append(m.cursors, 0)
		return m, tea.ClearScreen
	case res.Cmd != nil:
		return m, res.Cmd
	}
	return m, nil
}

func (m *model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, m.quitCmd()
	case "esc":
		m.step = stepMenu
		m.input.reset()
		return m, nil
	case "enter":
		v := strings.TrimSpace(m.input.value)
		if v == "" {
			return m, nil
		}
		fn := m.inputOnSave
		m.step = stepMenu
		m.input.reset()
		if fn != nil {
			return m, fn(v)
		}
		return m, nil
	case "backspace":
		m.input.backspace()
		return m, nil
	default:
		m.input.insert(msg)
		return m, nil
	}
}

// updatePreflight handles keys while a connection attempt is on screen. A
// bridge attempt starts from a guessed URL, so the user can type the real one
// over it instead of waiting for the guess to fail.
func (m *model) updatePreflight(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, m.quitCmd()
	}
	if !m.act.cancelable() {
		return m, nil
	}
	if msg.String() == "esc" {
		return m.cancelActivation()
	}
	if !m.act.overridable() {
		return m, nil
	}
	switch msg.String() {
	case "enter":
		v := strings.TrimSpace(m.act.override.value)
		if v == "" {
			return m, nil
		}
		return m.overrideActivationURL(v)
	case "backspace":
		m.act.override.backspace()
		return m, nil
	default:
		m.act.override.insert(msg)
		return m, nil
	}
}

func (m *model) viewPreflight() string {
	label := "Checking " + m.g.ApertureHost + " ..."
	if m.act != nil && m.act.label != "" {
		label = m.act.label
	}
	var sb strings.Builder
	sb.WriteString(m.wrapText("", dotYellow+" "+label) + "\n")
	for _, line := range m.bridgeLogs {
		sb.WriteString(dimStyle.Render(m.wrapText("  ", line)))
		sb.WriteString("\n")
	}
	switch {
	case m.act.overridable():
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render(m.wrapText("  ", "Different Aperture URL? Type it to connect there instead.")))
		sb.WriteString("\n")
		sb.WriteString("  > " + m.act.override.value + "█\n")
		if m.act.override.err != "" {
			sb.WriteString(errorStyle.Render(m.wrapText("  ", m.act.override.err)))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Enter to switch · Esc to cancel\n"))
	case m.act.cancelable():
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Esc to cancel\n"))
	}
	return sb.String()
}

func (m *model) View() string {
	switch m.step {
	case stepPreflight:
		return m.viewPreflight()
	case stepError:
		var sb strings.Builder
		sb.WriteString(errorStyle.Render("Error"))
		sb.WriteString("\n\n")
		sb.WriteString(m.wrapText("", m.errMsg))
		sb.WriteString("\n\n")
		sb.WriteString(dimStyle.Render("Any key to go back · q to quit\n"))
		return sb.String()
	case stepInput:
		var sb strings.Builder
		sb.WriteString(titleStyle.Render(m.inputTitle))
		sb.WriteString("\n")
		if m.inputPrompt != "" {
			sb.WriteString("  " + m.inputPrompt + "\n")
		}
		sb.WriteString("  > " + m.input.value + "█\n")
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Enter to save · Esc to cancel\n"))
		return sb.String()
	case stepMenu:
		return m.viewMenu()
	}
	return ""
}

func (m *model) viewMenu() string {
	top := m.top()
	if top == nil {
		return ""
	}
	var sb strings.Builder
	if header := m.menuHeader(top); header != "" {
		sb.WriteString(header)
	}
	if top.Title != "" {
		sb.WriteString(titleStyle.Render(top.Title))
		sb.WriteString("\n")
	}
	if top.Preamble != "" {
		for _, line := range strings.Split(top.Preamble, "\n") {
			sb.WriteString(dimStyle.Render(m.wrapText("  ", line)))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	cursor := m.cursor()
	tokens := assignTokens(top.Items)
	visible, twoCols, half := m.menuLayout(top)

	plains := make(map[int]string, len(visible))
	styleds := make(map[int]string, len(visible))
	maxW := 0
	for _, i := range visible {
		it := top.Items[i]
		tok := tokens[i]
		if tok == "" {
			tok = " "
		}
		plain := fmt.Sprintf("  [%s] %s", tok, it.Label)
		if it.Description != "" {
			plain += "  " + it.Description
		}
		styled := fmt.Sprintf("  [%s] %s", tok, it.Label)
		if it.Description != "" {
			styled += "  " + dimStyle.Render(it.Description)
		}
		if it.Disabled {
			styled = dimStyle.Render(styled)
		} else if i == cursor {
			styled = selectedStyle.Render(styled)
		}
		plains[i] = plain
		styleds[i] = styled
		if w := len(plain); w > maxW {
			maxW = w
		}
	}

	if twoCols {
		colWidth := maxW + 4
		for r := 0; r < half; r++ {
			li := visible[r]
			sb.WriteString(styleds[li])
			sb.WriteString(strings.Repeat(" ", colWidth-len(plains[li])))
			if r+half < len(visible) {
				ri := visible[r+half]
				sb.WriteString(styleds[ri])
			}
			sb.WriteString("\n")
		}
	} else {
		for _, i := range visible {
			sb.WriteString(styleds[i])
			sb.WriteString("\n")
			if top.Items[i].Digit == menu.DigitZero {
				sb.WriteString("\n")
			}
		}
	}
	sb.WriteString("\n")
	if top.Hint != "" {
		sb.WriteString(dimStyle.Render(top.Hint))
		sb.WriteString("\n")
	}
	if len(m.stack) == 1 && m.buildVersion != "" {
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Aperture " + m.buildVersion))
		sb.WriteString("\n")
	}
	return sb.String()
}

// menuLayout decides the visible order and column layout for a menu.
// visible is the list of Items indices that render (hidden rows skipped);
// twoCols is true when the wide-terminal / long-list two-column layout is
// active; half is len(visible) rounded up / 2 (the row count in each
// column). twoCols=false means half is unused.
func (m *model) menuLayout(top *menu.Menu) (visible []int, twoCols bool, half int) {
	visible = make([]int, 0, len(top.Items))
	hasZero := false
	for i, it := range top.Items {
		if it.Hidden {
			continue
		}
		if it.Digit == menu.DigitZero {
			hasZero = true
		}
		visible = append(visible, i)
	}
	if m.width < 80 || len(visible) < 10 || hasZero {
		return visible, false, 0
	}
	tokens := assignTokens(top.Items)
	maxW := 0
	for _, i := range visible {
		it := top.Items[i]
		tok := tokens[i]
		if tok == "" {
			tok = " "
		}
		w := len("  [] ") + len(tok) + len(it.Label)
		if it.Description != "" {
			w += 2 + len(it.Description)
		}
		if w > maxW {
			maxW = w
		}
	}
	if maxW*2+4 > m.width {
		return visible, false, 0
	}
	return visible, true, (len(visible) + 1) / 2
}

// visiblePos returns i's position within visible, or -1 if i isn't there.
func visiblePos(visible []int, i int) int {
	for p, v := range visible {
		if v == i {
			return p
		}
	}
	return -1
}

// autoTokens is the pool of single-character keys auto-assigned to menu
// items in visible order: 1-9, then a-z, then A-Z. "0" is reserved for the
// DigitZero pin; items that set an explicit Shortcut keep that key out of
// the pool.
var autoTokens = func() []string {
	var out []string
	for c := '1'; c <= '9'; c++ {
		out = append(out, string(c))
	}
	for c := 'a'; c <= 'z'; c++ {
		out = append(out, string(c))
	}
	for c := 'A'; c <= 'Z'; c++ {
		out = append(out, string(c))
	}
	return out
}()

// assignTokens returns one token per Items slot. Hidden or disabled items
// and items without an Action get an empty string. Items with DigitZero get
// "0"; items with Digit>0 get that digit (legacy explicit assignments).
// Everything else is auto-numbered from the autoTokens pool, skipping any
// token already claimed by an item's Shortcut or explicit Digit.
func assignTokens(items []menu.MenuItem) []string {
	tokens := make([]string, len(items))
	reserved := map[string]bool{}
	for _, it := range items {
		if it.Shortcut != "" {
			reserved[it.Shortcut] = true
		}
		if it.Digit > 0 {
			reserved[fmt.Sprintf("%d", it.Digit)] = true
		}
	}
	pool := make([]string, 0, len(autoTokens))
	for _, t := range autoTokens {
		if !reserved[t] {
			pool = append(pool, t)
		}
	}
	next := 0
	for i, it := range items {
		if it.Hidden || it.Disabled || it.Action == nil {
			continue
		}
		switch {
		case it.Digit == menu.DigitZero:
			tokens[i] = "0"
		case it.Digit > 0:
			tokens[i] = fmt.Sprintf("%d", it.Digit)
		default:
			if next < len(pool) {
				tokens[i] = pool[next]
				next++
			}
		}
	}
	return tokens
}

// menuHeader returns the one-line status banner shown above certain menus:
// the root menu shows the connected endpoint; the endpoints menu in
// preflight-failure mode shows the red "couldn't reach" banner.
func (m *model) menuHeader(top *menu.Menu) string {
	if len(m.stack) == 1 && top.Title == rootTitle {
		header := dotGreen + " Connected to " + m.endpointLabel(m.g.ActiveEndpoint())
		if n := len(m.g.Providers); n > 0 {
			header += fmt.Sprintf(" (%d providers)", n)
		}
		return m.wrapText("", header) + "\n\n"
	}
	if m.forcedToEndpoint && (top.Title == endpointsTitle || top.Title == setupGuideTitle) {
		target := m.g.ActiveEndpoint()
		if m.failedEndpoint != nil {
			target = *m.failedEndpoint
		}
		header := m.wrapText("", dotRed+" Could not reach "+m.endpointLabel(target)) + "\n"
		if m.preflightErr != "" {
			header += dimStyle.Render(m.wrapText("  ", m.preflightErr)) + "\n"
		}
		if m.g.Debug {
			for _, line := range m.bridgeLogs {
				header += dimStyle.Render(m.wrapText("  ", line)) + "\n"
			}
		}
		return header + "\n"
	}
	return ""
}

// wrapText wraps application output before Bubble Tea's renderer sees it.
// Bubble Tea truncates over-width lines rather than wrapping them, which can
// otherwise remove the useful end of a bridge error. Continuation lines keep
// the same indentation as the first line.
func (m *model) wrapText(indent, text string) string {
	if m.width <= 0 {
		return indent + text
	}
	indentWidth := ansi.StringWidth(indent)
	if indentWidth >= m.width {
		indent = ""
		indentWidth = 0
	}
	wrapped := ansi.Wrap(text, m.width-indentWidth, "")
	return indent + strings.ReplaceAll(wrapped, "\n", "\n"+indent)
}

// --- Stack helpers ---

func (m *model) top() *menu.Menu {
	if len(m.stack) == 0 {
		return nil
	}
	return m.stack[len(m.stack)-1]
}

func (m *model) cursor() int {
	if len(m.cursors) == 0 {
		return 0
	}
	return m.cursors[len(m.cursors)-1]
}

func (m *model) setCursor(c int) {
	if len(m.cursors) == 0 {
		return
	}
	m.cursors[len(m.cursors)-1] = c
}

func (m *model) popOne() {
	if len(m.stack) <= 1 {
		return
	}
	m.stack = m.stack[:len(m.stack)-1]
	m.cursors = m.cursors[:len(m.cursors)-1]
}

func (m *model) popToRoot() {
	if len(m.stack) > 1 {
		m.stack = m.stack[:1]
		m.cursors = m.cursors[:1]
	}
}

func (m *model) resetStack(root *menu.Menu) {
	m.stack = []*menu.Menu{root}
	m.cursors = []int{0}
}

func (m *model) refreshEndpointsMenu() {
	m.refreshMenuByTitle(endpointsTitle, m.endpointsMenu())
}

func (m *model) refreshBridgesMenu() {
	for i := range m.stack {
		if m.stack[i].Title == "Choose a bridge" {
			m.stack[i] = m.endpointBridgeMenu()
			m.cursors[i] = 0
		}
	}
	m.refreshMenuByTitle("Bridges", m.bridgesMenu())
}

func (m *model) refreshMenuByTitle(title string, next *menu.Menu) {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if m.stack[i].Title != title {
			continue
		}
		m.stack = m.stack[:i+1]
		m.cursors = m.cursors[:i+1]
		m.stack[i] = next
		m.cursors[i] = 0
		return
	}
	if len(m.stack) > 0 {
		m.stack[len(m.stack)-1] = next
		m.cursors[len(m.cursors)-1] = 0
		return
	}
	m.resetStack(next)
}

// --- Input step helpers ---

// promptForInput sets up the single-line text input step. initial is the
// editable starting value, empty for a blank field. onSave is invoked with the
// entered value when the user presses Enter.
func (m *model) promptForInput(title, prompt, initial string, onSave func(value string) tea.Cmd) {
	m.step = stepInput
	m.inputTitle = title
	m.inputPrompt = prompt
	m.input = textField{value: initial}
	m.inputOnSave = onSave
}

// --- Registered clients access ---

// registeredClients is the set visible to the TUI; overridable in tests.
var registeredClients = func(g *config.Global) []clients.Client {
	return clients.All(g)
}
