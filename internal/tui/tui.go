// Package tui is the bubbletea-driven interactive launcher. It renders a
// generic navigable menu stack described by internal/menu; each entry on
// the stack comes from either the root client picker (built from
// internal/clients) or a sub-menu pushed by a client's action closure.
// The TUI owns only the preflight HTTP check, a single-line text input
// step, and error screens — everything else is expressed as Menu values.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
	"github.com/tailscale/aperture-cli/internal/portals"
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

// NewModel returns the TUI model. g holds the persisted launcher state
// (settings, endpoints, last launch). buildVersion is shown at the bottom
// of the client picker.
func NewModel(g *config.Global, buildVersion string, portalManager *portals.Manager) tea.Model {
	return &model{
		g:             g,
		buildVersion:  buildVersion,
		portalManager: portalManager,
		step:          stepPreflight,
	}
}

type model struct {
	g             *config.Global
	buildVersion  string
	portalManager *portals.Manager

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
	inputValue  string
	inputOnSave func(value string) tea.Cmd

	// Error screen state.
	errMsg string

	// Preflight state.
	preflightErr     string
	forcedToEndpoint bool // true when preflight failure dropped user on endpoints menu
	preflightLabel   string
	portalLogCh      chan string
	portalLogs       []string
	portalCancel     context.CancelFunc
}

func (m *model) Init() tea.Cmd {
	return m.activateEndpointCmd(m.g.ActiveEndpoint())
}

// preflightResult is emitted when the /api/providers check completes.
type preflightResult struct {
	host      string
	providers []config.ProviderInfo
	err       error
}

type endpointActivationResult struct {
	endpoint  config.Endpoint
	host      string
	providers []config.ProviderInfo
	err       error
}

type portalLogMsg string
type portalLogDoneMsg struct{}
type quitMsg struct{ Err error }

func runPreflight(host string) tea.Cmd {
	return func() tea.Msg {
		provs, err := fetchProviders(host)
		return preflightResult{host: host, providers: provs, err: err}
	}
}

func fetchProviders(host string) ([]config.ProviderInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	url := strings.TrimRight(host, "/") + "/api/providers"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var provs []config.ProviderInfo
	if err := json.Unmarshal(body, &provs); err != nil {
		return nil, fmt.Errorf("could not parse providers response: %w", err)
	}
	return provs, nil
}

func (m *model) activateEndpointCmd(ep config.Endpoint) tea.Cmd {
	m.step = stepPreflight
	m.preflightErr = ""
	m.portalLogs = nil
	m.portalLogCh = nil
	if m.portalCancel != nil {
		m.portalCancel()
		m.portalCancel = nil
	}

	if ep.PortalID == "" {
		m.preflightLabel = "Checking " + ep.URL + " ..."
		return func() tea.Msg {
			provs, err := fetchProviders(ep.URL)
			return endpointActivationResult{endpoint: ep, host: ep.URL, providers: provs, err: err}
		}
	}

	portal, ok := m.g.Portal(ep.PortalID)
	if !ok {
		m.preflightLabel = "Checking " + ep.URL + " ..."
		return func() tea.Msg {
			return endpointActivationResult{
				endpoint: ep,
				host:     ep.URL,
				err:      fmt.Errorf("portal %s is not configured", ep.PortalID),
			}
		}
	}
	if m.portalManager == nil {
		return func() tea.Msg {
			return endpointActivationResult{
				endpoint: ep,
				host:     ep.URL,
				err:      fmt.Errorf("portal manager is not configured"),
			}
		}
	}

	ch := make(chan string, 32)
	ctx, cancel := context.WithCancel(context.Background())
	m.portalLogCh = ch
	m.portalCancel = cancel
	m.preflightLabel = "Connecting portal " + portal.Name + " to " + ep.URL + " ..."
	activate := func() tea.Msg {
		defer cancel()
		defer close(ch)
		localURL, err := m.portalManager.Activate(ctx, portal, ep.URL, func(line string) {
			line = strings.TrimSpace(line)
			if line == "" {
				return
			}
			select {
			case ch <- line:
			default:
			}
		})
		if err != nil {
			return endpointActivationResult{endpoint: ep, host: ep.URL, err: err}
		}
		provs, err := fetchProviders(localURL)
		return endpointActivationResult{endpoint: ep, host: localURL, providers: provs, err: err}
	}
	return tea.Batch(activate, waitPortalLog(ch))
}

func waitPortalLog(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return portalLogDoneMsg{}
		}
		return portalLogMsg(line)
	}
}

func (m *model) quitCmd() tea.Cmd {
	cancel := m.portalCancel
	portalManager := m.portalManager
	return func() tea.Msg {
		if cancel != nil {
			cancel()
		}
		if portalManager == nil {
			return quitMsg{}
		}
		return quitMsg{Err: portalManager.Close()}
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
			m.preflightErr = msg.err.Error()
			m.forcedToEndpoint = true
			m.step = stepMenu
			m.resetStack(m.setupGuideMenu())
			return m, nil
		}
		m.g.Providers = msg.providers
		m.preflightErr = ""
		m.forcedToEndpoint = false
		m.step = stepMenu
		m.resetStack(m.rootMenu())
		return m, tea.ClearScreen

	case endpointActivationResult:
		m.portalCancel = nil
		if msg.err != nil {
			m.preflightErr = msg.err.Error()
			m.forcedToEndpoint = true
			m.g.ApertureHost = msg.endpoint.URL
			m.step = stepMenu
			m.resetStack(m.setupGuideMenu())
			return m, nil
		}
		m.g.ApertureHost = msg.host
		m.g.Providers = msg.providers
		m.preflightErr = ""
		m.forcedToEndpoint = false
		m.step = stepMenu
		m.resetStack(m.rootMenu())
		return m, tea.ClearScreen

	case portalLogMsg:
		m.portalLogs = append(m.portalLogs, string(msg))
		if len(m.portalLogs) > 12 {
			m.portalLogs = m.portalLogs[len(m.portalLogs)-12:]
		}
		if m.portalLogCh != nil {
			return m, waitPortalLog(m.portalLogCh)
		}
		return m, nil

	case portalLogDoneMsg:
		m.portalLogCh = nil
		return m, nil

	case quitMsg:
		if msg.Err != nil {
			m.errMsg = "Error shutting down portals: " + msg.Err.Error()
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
		m.preflightLabel = "Checking " + m.g.ApertureHost + " ..."
		return m, runPreflight(m.g.ApertureHost)

	case menu.InstallDoneMsg:
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
			if msg.String() == "ctrl+c" {
				return m, m.quitCmd()
			}
			return m, nil
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
		m.inputValue = ""
		return m, nil
	case "enter":
		v := strings.TrimSpace(m.inputValue)
		if v == "" {
			return m, nil
		}
		fn := m.inputOnSave
		m.step = stepMenu
		m.inputValue = ""
		if fn != nil {
			return m, fn(v)
		}
		return m, nil
	case "backspace":
		if len(m.inputValue) > 0 {
			m.inputValue = m.inputValue[:len(m.inputValue)-1]
		}
		return m, nil
	default:
		s := msg.String()
		if len(s) == 1 {
			m.inputValue += s
		}
		return m, nil
	}
}

func (m *model) View() string {
	switch m.step {
	case stepPreflight:
		label := m.preflightLabel
		if label == "" {
			label = "Checking " + m.g.ApertureHost + " ..."
		}
		var sb strings.Builder
		sb.WriteString(dotYellow + " " + label + "\n")
		for _, line := range m.portalLogs {
			sb.WriteString(dimStyle.Render("  " + line))
			sb.WriteString("\n")
		}
		return sb.String()
	case stepError:
		var sb strings.Builder
		sb.WriteString(errorStyle.Render("Cannot launch"))
		sb.WriteString("\n\n")
		sb.WriteString(m.errMsg)
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
		sb.WriteString("  > " + m.inputValue + "█\n")
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
			sb.WriteString(dimStyle.Render("  " + line))
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
		header := dotGreen + " Connected to " + m.g.ApertureHost
		if n := len(m.g.Providers); n > 0 {
			header += fmt.Sprintf(" (%d providers)", n)
		}
		return header + "\n\n"
	}
	if m.forcedToEndpoint && (top.Title == endpointsTitle || top.Title == setupGuideTitle) {
		header := dotRed + " Could not reach " + m.g.ApertureHost + "\n"
		if m.preflightErr != "" && top.Title != setupGuideTitle {
			header += dimStyle.Render("  "+m.preflightErr) + "\n"
		}
		return header + "\n"
	}
	return ""
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

func (m *model) refreshPortalsMenu() {
	for i := range m.stack {
		if m.stack[i].Title == "Choose a portal" {
			m.stack[i] = m.endpointPortalMenu()
			m.cursors[i] = 0
		}
	}
	m.refreshMenuByTitle("Portals", m.portalsMenu())
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

// promptForInput sets up the single-line text input step. onSave is invoked
// with the entered value when the user presses Enter.
func (m *model) promptForInput(title, prompt string, onSave func(value string) tea.Cmd) {
	m.step = stepInput
	m.inputTitle = title
	m.inputPrompt = prompt
	m.inputValue = ""
	m.inputOnSave = onSave
}

// --- Registered clients access ---

// registeredClients is the set visible to the TUI; overridable in tests.
var registeredClients = func(g *config.Global) []clients.Client {
	return clients.All(g)
}
