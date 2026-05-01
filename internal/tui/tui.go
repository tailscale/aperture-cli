// Package tui is the bubbletea-driven interactive launcher. It renders a
// generic navigable menu stack described by internal/menu; each entry on
// the stack comes from either the root client picker (built from
// internal/clients) or a sub-menu pushed by a client's action closure.
// The TUI owns only the preflight HTTP check, a single-line text input
// step, and error screens — everything else is expressed as Menu values.
package tui

import (
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
func NewModel(g *config.Global, buildVersion string) tea.Model {
	return &model{
		g:            g,
		buildVersion: buildVersion,
		step:         stepPreflight,
	}
}

type model struct {
	g            *config.Global
	buildVersion string

	step step

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
}

func (m *model) Init() tea.Cmd {
	return runPreflight(m.g.ApertureHost)
}

// preflightResult is emitted when the /api/providers check completes.
type preflightResult struct {
	host      string
	providers []config.ProviderInfo
	err       error
}

func runPreflight(host string) tea.Cmd {
	return func() tea.Msg {
		client := &http.Client{Timeout: 10 * time.Second}
		url := strings.TrimRight(host, "/") + "/api/providers"
		resp, err := client.Get(url)
		if err != nil {
			return preflightResult{host: host, err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return preflightResult{
				host: host,
				err:  fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url),
			}
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return preflightResult{host: host, err: err}
		}
		var provs []config.ProviderInfo
		if err := json.Unmarshal(body, &provs); err != nil {
			return preflightResult{host: host, err: fmt.Errorf("could not parse providers response: %w", err)}
		}
		return preflightResult{host: host, providers: provs}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case preflightResult:
		if msg.err != nil {
			m.preflightErr = msg.err.Error()
			m.forcedToEndpoint = true
			m.step = stepMenu
			m.resetStack(m.endpointsMenu())
			return m, nil
		}
		m.g.Providers = msg.providers
		m.preflightErr = ""
		m.forcedToEndpoint = false
		// Ensure the active host is in the endpoint list and first.
		_ = m.g.UpsertEndpoint(m.g.ApertureHost)
		m.step = stepMenu
		m.resetStack(m.rootMenu())
		return m, tea.ClearScreen

	case menu.ExecDoneMsg:
		// A client's foreground launch has exited. Re-run preflight: the
		// user may have changed things outside the launcher while the
		// agent was running.
		m.popToRoot()
		m.step = stepPreflight
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
				return m, tea.Quit
			}
			return m, nil
		case stepError:
			switch msg.String() {
			case "ctrl+c", "q":
				return m, tea.Quit
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
		return m, tea.Quit

	case "q":
		// "q" quits from the root only; on sub-menus it pops.
		if len(m.stack) <= 1 {
			return m, tea.Quit
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
		if cursor > 0 {
			m.setCursor(cursor - 1)
			m.skipHiddenUp()
		}
		return m, nil

	case "down", "j":
		if cursor < len(top.Items)-1 {
			m.setCursor(cursor + 1)
			m.skipHiddenDown()
		}
		return m, nil

	case "enter":
		return m.activate(cursor)

	default:
		s := msg.String()
		// Digit shortcut: activate item with matching Digit. Auto-numbered
		// items count in visible order, skipping any item with an explicit
		// Digit assignment.
		if len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
			auto := 1
			for i, it := range top.Items {
				if it.Hidden || it.Disabled {
					continue
				}
				var d int
				switch it.Digit {
				case 0:
					d = auto
					auto++
				case menu.DigitZero:
					d = 0
				default:
					d = it.Digit
				}
				if s[0]-'0' == byte(d) {
					return m.activate(i)
				}
			}
		}
		// Single-char shortcut.
		if len(s) == 1 {
			for i, it := range top.Items {
				if it.Hidden || it.Disabled {
					continue
				}
				if it.Shortcut != "" && it.Shortcut == s {
					return m.activate(i)
				}
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
	m.setCursor(idx)
	res := item.Action()
	return m.applyResult(res)
}

func (m *model) applyResult(res menu.Result) (tea.Model, tea.Cmd) {
	switch {
	case res.Quit:
		return m, tea.Quit
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
		return m, tea.Quit
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
		return dotYellow + " Checking " + m.g.ApertureHost + " …\n"
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
	cursor := m.cursor()
	auto := 1
	for i, it := range top.Items {
		if it.Hidden {
			continue
		}
		var d int
		isZero := false
		switch it.Digit {
		case 0:
			d = auto
			auto++
		case menu.DigitZero:
			d = 0
			isZero = true
		default:
			d = it.Digit
		}
		label := fmt.Sprintf("  [%d] %s", d, it.Label)
		if it.Description != "" {
			label += "  " + dimStyle.Render(it.Description)
		}
		if it.Disabled {
			label = dimStyle.Render(label)
		} else if i == cursor {
			label = selectedStyle.Render(label)
		}
		sb.WriteString(label)
		sb.WriteString("\n")
		if isZero {
			sb.WriteString("\n")
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
	if m.forcedToEndpoint && top.Title == endpointsTitle {
		header := dotRed + " Could not reach " + m.g.ApertureHost + "\n"
		if m.preflightErr != "" {
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

// skipHiddenUp advances the cursor backward past hidden items.
func (m *model) skipHiddenUp() {
	top := m.top()
	if top == nil {
		return
	}
	c := m.cursor()
	for c > 0 && top.Items[c].Hidden {
		c--
	}
	m.setCursor(c)
}

func (m *model) skipHiddenDown() {
	top := m.top()
	if top == nil {
		return
	}
	c := m.cursor()
	for c < len(top.Items)-1 && top.Items[c].Hidden {
		c++
	}
	m.setCursor(c)
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
