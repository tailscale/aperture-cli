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
	"github.com/tailscale/aperture-cli/internal/connection"
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
	// authStyle is the login link at the foot of the connect screen: the
	// palette's bright green on a dark terminal, its plain green on a light
	// one, where bright green is unreadable.
	authStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "2", Dark: "10"})

	dotYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("●")
	dotGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("●")
	dotRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("●")
)

// NewModel returns the TUI model. start is the endpoint to open on, which is
// the saved active one unless the invocation named another.
func NewModel(g *config.Global, buildVersion string, machines *bridges.Machines, start config.Endpoint) tea.Model {
	return &model{
		g:            g,
		buildVersion: buildVersion,
		machines:     machines,
		start:        start,
		step:         stepPreflight,
	}
}

type model struct {
	g            *config.Global
	buildVersion string
	machines     *bridges.Machines
	// start is not necessarily in settings yet: one named on the command line
	// is written on the way in and taken back out if the attempt is abandoned,
	// same as one typed into the connection picker.
	start config.Endpoint

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
	bridgeLogs       []bridgeLine
	failedEndpoint   *config.Endpoint
	connected        bool
}

// activation is the connection attempt currently on screen: its identity, its
// cancellation handle, and the URL the user can type over the top of it. The
// log tail stays on the model because the failure screen outlives the attempt.
//
// cancel is nil for attempts that cannot be interrupted (the post-launch
// re-check), which is what makes Esc and the inline override inert there.
type activation struct {
	id int
	// attempt is the ConnectionAttempt this screen shows. Nil for the wait a
	// bridge removal puts on the same screen.
	attempt *bridges.Attempt
	label   string
	started time.Time
	cancel  context.CancelFunc
	logCh   chan bridgeLine
	logCtx  context.Context
	// phase is the wait this attempt is in, and phaseSet distinguishes "not
	// started" from StartingMachine, which is the zero value.
	phase    connection.Phase
	phaseSet bool
	// authURL is the Tailscale login link already surfaced for this attempt.
	// The control plane can re-send it on the bus, so this is what keeps the
	// browser from being opened again on each repeat.
	authURL string
	// copied records that the login link reached the terminal's clipboard, so
	// the copy button can say so. A click that does nothing visible reads as a
	// button that does not work.
	copied bool
	// override is the inline "different Aperture URL" editor shown while a
	// bridge attempt runs.
	override textField
}

// logLine stamps a line the TUI itself produces (a browser or clipboard
// failure) against the same clock the bridge's own lines are stamped with.
func (a *activation) logLine(text string) bridgeLine {
	return bridgeLine{elapsed: time.Since(a.started), event: connection.Note(text)}
}

// entered records a phase the attempt moved into, and reports whether it moved.
// The attempt owns the rule rather than the bridge because phases arrive from
// both the IPN bus and the manager, and only something seeing both can order
// them. A bus that re-notifies NeedsLogin would otherwise walk the user back.
func (a *activation) entered(p connection.Phase) bool {
	if a.phaseSet && p <= a.phase {
		return false
	}
	a.phase, a.phaseSet = p, true
	return true
}

// endpoint is the Endpoint the attempt on screen is trying, zero when the
// screen is showing something else.
func (a *activation) endpoint() config.Endpoint {
	if a == nil || a.attempt == nil {
		return config.Endpoint{}
	}
	return a.attempt.Endpoint
}

// cancelable reports whether Esc can interrupt this attempt.
func (a *activation) cancelable() bool { return a != nil && a.cancel != nil }

// overridable reports whether the attempt accepts a typed URL in place of the
// one being probed. Only bridge attempts start from a guessed URL.
func (a *activation) overridable() bool { return a.cancelable() && a.endpoint().BridgeID != "" }

// bridging is the Connection context's service over this program's settings
// and Machines. Stateless, so built where it is used.
func (m *model) bridging() bridges.Bridging {
	return bridges.Bridging{Machines: m.machines, Settings: m.g}
}

// textField is the shared single-line editor behind the add-endpoint input
// step and the inline URL override on the connect screen.
type textField struct {
	value string
	err   string
}

// insert appends the text a key press carries. A pasted URL arrives as many
// runes in one message, and dropping it leaves the user retyping an endpoint by
// hand. Named keys and Alt chords carry no text: matching on the key's String()
// would append "up" when someone presses Up.
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

// Init opens on m.start. connectVia because an endpoint off the command line
// may not be in settings yet, and it writes it there for the failure screen to
// name; for the saved endpoint the two calls are the same.
func (m *model) Init() tea.Cmd {
	return m.connectVia(m.start, false)
}

// endpointActivationResult is how an attempt's outcome reaches the update
// loop, where settings may be written.
type endpointActivationResult struct {
	// id identifies the attempt this result belongs to. A result whose id no
	// longer matches the current attempt is stale: the user cancelled it or
	// typed a different URL over it, and its outcome must not be applied.
	id       int
	verified bridges.Verified
	err      error
}

// bridgeLine is one thing the attempt reported and how far into the attempt it
// was. The elapsed time is why this is not a string: a bridge that takes half a
// minute spends it in the control plane, the browser or the first dial, and an
// unstamped log cannot say which. Three fixes were aimed without knowing.
type bridgeLine struct {
	elapsed time.Duration
	event   connection.Event
}

// String renders a log line the way the connect screen shows it.
func (l bridgeLine) String() string {
	return fmt.Sprintf("+%-6s %s", l.elapsed.Round(100*time.Millisecond), l.event)
}

type bridgeLogMsg struct {
	ch   chan bridgeLine
	line bridgeLine
}
type bridgeLogDoneMsg struct{ ch chan bridgeLine }

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

// clipboardMsg reports the outcome of a click on the login link's copy button.
type clipboardMsg struct {
	id  int
	err error
}

func copyURLCmd(id int, url string) tea.Cmd {
	return func() tea.Msg { return clipboardMsg{id: id, err: copyToClipboard(url)} }
}

// activationTickMsg repaints the connect screen once a second so a slow attempt
// is visibly still running. Bring-up and the model fetch can take tens of
// seconds logging nothing, and a frozen screen looks like a hang.
type activationTickMsg struct{ id int }

func activationTick(id int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return activationTickMsg{id: id} })
}

type quitMsg struct{ Err error }

// activateEndpointCmd connects to ep. When ep is the attempt already on
// screen, this is a retry and keeps what that attempt knows: the original of
// a pending edit and whether it wrote ep into settings.
func (m *model) activateEndpointCmd(ep config.Endpoint) tea.Cmd {
	if m.act != nil && m.act.attempt != nil && config.SameEndpoint(m.act.endpoint(), ep) {
		return m.startAttempt(m.act.attempt.Retry())
	}
	return m.connect(ep, false, nil)
}

// connect begins an attempt at ep and puts it on screen. switchTailnet logs
// the bridge out first, so the attempt starts from a login prompt rather than
// the tailnet it is on. replacing is the original of a URL edit.
func (m *model) connect(ep config.Endpoint, switchTailnet bool, replacing *config.Endpoint) tea.Cmd {
	a, err := m.bridging().Begin(ep, switchTailnet, replacing)
	if err != nil {
		return simpleErrorCmd(err)
	}
	return m.startAttempt(a)
}

// start puts a prepared attempt on the connect screen and runs it. The
// attempt's outcome comes back as an endpointActivationResult and is applied
// there, on this loop, where settings are read.
func (m *model) startAttempt(a *bridges.Attempt) tea.Cmd {
	m.stopActivation()
	m.step = stepPreflight
	m.preflightErr = ""
	m.bridgeLogs = nil
	if a.InvalidatesActive {
		m.connected = false
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.activationSeq++
	act := &activation{
		id:      m.activationSeq,
		attempt: a,
		label:   "Checking " + a.Endpoint.URL + " ...",
		started: time.Now(),
		cancel:  cancel,
	}
	m.act = act
	bridging := m.bridging()

	if a.Endpoint.BridgeID == "" {
		run := func() tea.Msg {
			defer cancel()
			v, err := bridging.Run(ctx, a, nil)
			return endpointActivationResult{id: act.id, verified: v, err: err}
		}
		return tea.Batch(run, activationTick(act.id))
	}

	ch := make(chan bridgeLine, 32)
	act.logCh = ch
	act.logCtx = ctx
	act.label = "Connecting bridge " + a.Bridge().Name + " to " + a.Endpoint.URL + " ..."
	if a.SwitchesTailnet() {
		act.label = "Switching bridge " + a.Bridge().Name + " to a different tailnet ..."
	}
	emit := bridgeLogSink(ctx, ch, act.started)
	run := func() tea.Msg {
		defer cancel()
		v, err := bridging.Run(ctx, a, emit)
		return endpointActivationResult{id: act.id, verified: v, err: err}
	}
	return tea.Batch(run, waitBridgeLog(ctx, ch), activationTick(act.id))
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
	return m.bridging().Abandon(act.attempt)
}

// cancelActivation abandons the attempt on screen and returns to the menu the
// user started it from. At startup there is no such menu, so the setup guide
// takes its place.
func (m *model) cancelActivation() (tea.Model, tea.Cmd) {
	act := m.act
	if act == nil {
		return m, nil
	}
	endpoint := act.endpoint()
	if err := m.discardActivation(); err != nil {
		m.errMsg = "could not remove endpoint: " + err.Error()
		m.step = stepError
		return m, nil
	}
	m.act = nil
	m.step = stepMenu
	if len(m.stack) == 0 || !m.connected && m.g.ActiveEndpoint().BridgeID != "" {
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
	next, err := config.ParseEndpoint(value, act.endpoint().BridgeID)
	if err != nil {
		// Keep the running attempt: the typo costs nothing, and the guess
		// may still land while the user fixes it.
		act.override.err = err.Error()
		return m, nil
	}
	if config.SameEndpoint(next, act.endpoint()) {
		act.override.reset()
		return m, nil
	}
	return m, m.retargetActivation(next)
}

// retargetActivation replaces an attempt's candidate while retaining the
// original endpoint of a pending edit. Both URL editors use this path.
func (m *model) retargetActivation(next config.Endpoint) tea.Cmd {
	act := m.act
	if act == nil || act.attempt == nil {
		return m.connect(next, false, nil)
	}
	m.stopActivation()
	a, err := m.bridging().Retarget(act.attempt, next)
	if err != nil {
		m.errMsg = err.Error()
		m.step = stepError
		return nil
	}
	return m.startAttempt(a)
}

// bridgeLogSink is where the attempt's events land on their way to the update
// loop. Only diagnostics are dropped when the buffer is full; everything else
// waits for room, bounded by the attempt's cancellation. This sink used to drop
// whatever arrived, and under -debug tsnet's backend logger shares it, so a
// burst of chatter could take the login link with it.
func bridgeLogSink(ctx context.Context, ch chan<- bridgeLine, started time.Time) func(connection.Event) {
	return func(ev connection.Event) {
		if ev.Kind == connection.Noted {
			ev.Text = strings.TrimSpace(ev.Text)
			if ev.Text == "" {
				return
			}
		}
		// Stamped here rather than where the message is handled: a burst of
		// tsnet logs queues in the channel, and a stamp read after the queue
		// would attribute the queueing delay to the wrong line.
		line := bridgeLine{elapsed: time.Since(started), event: ev}
		select {
		case <-ctx.Done():
			return
		default:
		}
		if ev.Droppable() {
			// No ctx case: it was just checked, and a select that offers both
			// picks between them at random when the send would also succeed.
			select {
			case ch <- line:
			default:
			}
			return
		}
		select {
		case <-ctx.Done():
		case ch <- line:
		}
	}
}

func waitBridgeLog(ctx context.Context, ch chan bridgeLine) tea.Cmd {
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
	machines := m.machines
	return func() tea.Msg {
		if cancel != nil {
			cancel()
		}
		return quitMsg{Err: machines.Close()}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case endpointActivationResult:
		if m.act == nil || msg.id != m.act.id {
			// Cancelled or overridden: a newer attempt owns the screen.
			return m, nil
		}
		m.act.cancel = nil
		bridging := m.bridging()
		failed := m.act.endpoint()
		if msg.err != nil {
			if bridging.Fail(m.act.attempt) {
				m.connected = false
			}
			m.preflightErr = msg.err.Error()
			m.forcedToEndpoint = true
			m.failedEndpoint = &failed
			m.step = stepMenu
			m.resetStack(m.setupGuideMenu())
			return m, nil
		}
		if err := bridging.Commit(m.act.attempt, msg.verified); err != nil {
			m.preflightErr = err.Error()
			m.forcedToEndpoint = true
			m.failedEndpoint = &failed
			m.step = stepMenu
			m.resetStack(m.setupGuideMenu())
			return m, nil
		}
		m.connected = true
		m.preflightErr = ""
		m.forcedToEndpoint = false
		m.failedEndpoint = nil
		m.step = stepMenu
		m.resetStack(m.rootMenu())
		return m, tea.ClearScreen

	case bridgeRemovedMsg:
		return m.bridgeRemoved(msg)

	case bridgeLogMsg:
		if m.act == nil || m.act.logCh != msg.ch {
			return m, nil
		}
		next := waitBridgeLog(m.act.logCtx, m.act.logCh)
		switch msg.line.event.Kind {
		case connection.LoginRequired:
			url := msg.line.event.Link.String()
			if url == m.act.authURL {
				return m, next // the control plane re-sent the same link
			}
			// Not appended to the log tail: the footer owns the link now, and
			// two copies of a 60 character URL on one screen is noise.
			m.act.authURL = url
			m.act.copied = false
			return m, tea.Batch(next, openURLCmd(m.act.id, url))
		case connection.PhaseEntered:
			if !m.act.entered(msg.line.event.Phase) {
				return m, next
			}
		}
		m.bridgeLogs = appendBridgeLog(m.bridgeLogs, msg.line)
		return m, next

	case activationTickMsg:
		if m.step != stepPreflight || m.act == nil || m.act.id != msg.id {
			return m, nil
		}
		return m, activationTick(msg.id)

	case browserOpenMsg:
		// Only the failure is worth a line: a browser that opened is on the
		// user's screen and the link is already in the footer. Over SSH the
		// failure is the common case, not an edge case.
		if m.act == nil || m.act.id != msg.id || msg.err == nil {
			return m, nil
		}
		m.bridgeLogs = appendBridgeLog(m.bridgeLogs, m.act.logLine("Could not open a browser here ("+msg.err.Error()+"). Use the link below to authorize."))
		return m, nil

	case clipboardMsg:
		if m.act == nil || m.act.id != msg.id {
			return m, nil
		}
		if msg.err != nil {
			m.bridgeLogs = appendBridgeLog(m.bridgeLogs, m.act.logLine("Could not copy the link ("+msg.err.Error()+"). Select it above instead."))
			return m, nil
		}
		m.act.copied = true
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
		cmd := m.connect(m.g.ActiveEndpoint(), false, nil)
		// No cancel handle: this re-check owns the screen until it answers.
		if m.act != nil {
			m.act.cancel = nil
		}
		return m, cmd

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
func appendBridgeLog(logs []bridgeLine, line bridgeLine) []bridgeLine {
	logs = append(logs, line)
	for len(logs) > bridgeLogLimit {
		drop := 0
		for i, line := range logs {
			if !line.important() {
				drop = i
				break
			}
		}
		logs = append(logs[:drop], logs[drop+1:]...)
	}
	return logs
}

// important reports whether this line survives trimming. A phase always does:
// the phases are the record of where the time went, and evicting one to make
// room for tsnet chatter puts a gap in exactly the thing the log is for.
func (l bridgeLine) important() bool {
	return l.event.Kind != connection.Noted || importantBridgeLog(l.event.Text)
}

func importantBridgeLog(line string) bool {
	for _, prefix := range []string{
		"Could not open a browser here",
		"Could not copy the link",
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
	// Before the override editor gets a look: that editor owns every printable
	// key while a bridge attempt runs, which is the same screen the login link
	// appears on, so the copy key has to be a chord the editor drops.
	if msg.String() == "ctrl+y" && m.act != nil && m.act.authURL != "" {
		return m, copyURLCmd(m.act.id, m.act.authURL)
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

// authCopyHint and authCopiedHint are the line under the login link, before
// and after ctrl+y. The key has to be named on screen: nothing about a URL
// suggests which chord copies it.
const (
	authCopyHint   = "ctrl+y to copy the link"
	authCopiedHint = "✓ copied to the clipboard"
	authProse      = "Authorize this bridge in your browser:"
)

// authFooter renders the login link pinned to the foot of the connect screen.
//
// The link owns its lines outright. Bubble Tea truncates any line wider than
// the terminal, so a long URL has to wrap, and prose sharing those lines lands
// in the selection when the user drags across them; a browser strips a newline
// out of a URL but not an indent or a label. Every line carries the same OSC 8
// hyperlink, id-tagged so terminals rejoin the halves and ctrl-click survives.
func (m *model) authFooter() string {
	act := m.act
	if act == nil || act.authURL == "" {
		return ""
	}
	hint := authCopyHint
	if act.copied {
		hint = authCopiedHint
	}
	var sb strings.Builder
	// Styled a line at a time: lipgloss pads a multi-line block out to its
	// widest line, which would leave trailing spaces on a wrapped link.
	for _, line := range strings.Split(m.wrapText("", authProse), "\n") {
		sb.WriteString(authStyle.Render(line))
		sb.WriteString("\n")
	}
	for _, line := range strings.Split(m.wrapText("", act.authURL), "\n") {
		sb.WriteString(ansi.SetHyperlink(act.authURL, "id=aperture-auth"))
		sb.WriteString(authStyle.Render(line))
		sb.WriteString(ansi.ResetHyperlink())
		sb.WriteString("\n")
	}
	for i, line := range strings.Split(m.wrapText("", hint), "\n") {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(dimStyle.Render(line))
	}
	return sb.String()
}

// activationElapsed counts the attempt up on screen. It starts at 2s so a
// connection that answers immediately does not flash a counter.
func activationElapsed(act *activation) string {
	if act == nil || act.started.IsZero() {
		return ""
	}
	if secs := int(time.Since(act.started).Seconds()); secs >= 2 {
		return fmt.Sprintf(" (%ds)", secs)
	}
	return ""
}

func (m *model) viewPreflight() string {
	label := "Checking " + m.g.ApertureHost + " ..."
	if m.act != nil && m.act.label != "" {
		label = m.act.label
	}
	var sb strings.Builder
	sb.WriteString(m.wrapText("", dotYellow+" "+label+activationElapsed(m.act)) + "\n")
	for _, line := range m.bridgeLogs {
		sb.WriteString(dimStyle.Render(m.wrapText("  ", line.String())))
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
	if footer := m.authFooter(); footer != "" {
		sb.WriteString("\n")
		sb.WriteString(footer)
		sb.WriteString("\n")
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
				header += dimStyle.Render(m.wrapText("  ", line.String())) + "\n"
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
