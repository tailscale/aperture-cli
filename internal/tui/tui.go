package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tailscale/aperture-cli/internal/profiles"
)

type step int

const (
	stepPreflight          step = iota // checking /api/providers
	stepSelectAgent                    // choose profile
	stepSelectBackend                  // choose provider
	stepSettings                       // top-level settings menu
	stepEndpoints                      // manage aperture endpoints
	stepAddLocation                    // type a new endpoint URL
	stepAddLocationVertex              // optional vertex project ID for new endpoint
	stepInstall                        // show install hint for an uninstalled profile
	stepInstallAgents                  // choose an uninstalled profile to install
	stepUninstall                      // list installed profiles to uninstall
	stepUninstallConfirm               // confirm uninstall for a chosen profile
	stepEnterVertexProject             // prompt for Vertex project ID
	stepCheckError                     // pre-launch validation failure
	stepError                          // fatal error
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

// preflightResult carries the outcome of the /api/providers check.
type preflightResult struct {
	host      string
	providers []profiles.ProviderInfo
	err       error
}

type model struct {
	apertureHost string
	settings     profiles.Settings
	state        profiles.StateFile
	manager      *profiles.Manager

	// resolved combos for the last-used shortcut
	lastCombo *profiles.Combo

	// all known profiles; installedProfiles is the subset on PATH
	allProfiles       []profiles.Profile
	installedProfiles []profiles.Profile

	step          step
	agentCursor   int
	backendItems  []profiles.Backend
	backendCursor int

	chosenProfile profiles.Profile

	// preflight state
	preflightChecking bool
	providers         []profiles.ProviderInfo
	preflightErr      string

	// pendingLocationURL holds the URL staged during stepAddLocation
	// before transitioning to stepAddLocationVertex.
	pendingLocationURL string

	// endpointsFromSetup is true when stepEndpoints was reached via preflight failure.
	endpointsFromSetup bool

	// settings step
	settingsCursor int

	// endpoints submenu step
	endpointsCursor int

	// install agents submenu step
	installAgentsCursor int

	// uninstall submenu step
	uninstallCursor int

	// add-location step
	addLocationInput string

	// vertex project ID step
	vertexProjectInput string
	pendingCombo       *profiles.Combo

	debug bool
	err   string
}

// NewModel constructs the TUI model. It satisfies tea.Model.
func NewModel(apertureHost string, settings profiles.Settings, state profiles.StateFile, debug bool) tea.Model {
	mgr := profiles.NewManager()

	m := model{
		apertureHost:      apertureHost,
		settings:          settings,
		state:             state,
		manager:           mgr,
		allProfiles:       mgr.AllProfiles(),
		installedProfiles: mgr.InstalledProfiles(),
		debug:             debug,
		step:              stepPreflight,
		preflightChecking: true,
	}

	// Resolve last-used combo (only from installed profiles).
	if state.LastProfileName != "" && state.LastBackendType != "" {
		for _, p := range m.installedProfiles {
			if p.Name() == state.LastProfileName {
				for _, b := range p.SupportedBackends() {
					if string(b.Type) == state.LastBackendType {
						combo := profiles.Combo{Profile: p, Backend: b}
						m.lastCombo = &combo
					}
				}
			}
		}
	}

	return m
}

func (m model) Init() tea.Cmd {
	return runPreflight(m.apertureHost)
}

// runPreflight performs the GET {host}/api/providers check asynchronously.
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

		var providers []profiles.ProviderInfo
		if err := json.Unmarshal(body, &providers); err != nil {
			return preflightResult{host: host, err: fmt.Errorf("could not parse providers response: %w", err)}
		}
		return preflightResult{host: host, providers: providers}
	}
}

type autoSelectMsg struct{ combo profiles.Combo }
type execDoneMsg struct{ err error }
type installDoneMsg struct{ err error }
type uninstallDoneMsg struct{ err error }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case preflightResult:
		m.preflightChecking = false
		if msg.err != nil {
			m.preflightErr = msg.err.Error()
			m.endpointsFromSetup = true
			m.step = stepEndpoints
			m.endpointsCursor = 0
			return m, nil
		}
		// Success: store providers, update settings with confirmed host, proceed.
		m.providers = msg.providers
		m.preflightErr = ""
		m.endpointsFromSetup = false
		m.settings = upsertLocation(m.settings, m.apertureHost)
		_ = profiles.SaveSettings(m.settings)
		// Re-check which CLIs are installed now.
		m.installedProfiles = m.manager.InstalledProfiles()
		// Validate lastCombo against filtered backends.
		if m.lastCombo != nil {
			filtered := m.manager.FilteredBackends(m.lastCombo.Profile, m.providers)
			found := false
			for _, b := range filtered {
				if b.Type == m.lastCombo.Backend.Type {
					found = true
					break
				}
			}
			if !found {
				m.lastCombo = nil
			}
		}
		// If exactly one combo exists, jump straight to exec.
		combos := m.manager.ValidCombos(m.providers)
		if len(combos) == 1 {
			return m, func() tea.Msg { return autoSelectMsg{combo: combos[0]} }
		}
		m.step = stepSelectAgent
		return m, tea.ClearScreen

	case autoSelectMsg:
		return m.prepareAndExec(msg.combo)

	case installDoneMsg:
		// Re-check installed CLIs after the install command finishes.
		m.installedProfiles = m.manager.InstalledProfiles()
		m.step = stepSelectAgent
		m.agentCursor = 0
		return m, tea.ClearScreen

	case uninstallDoneMsg:
		// Re-check installed CLIs after the uninstall command finishes.
		m.installedProfiles = m.manager.InstalledProfiles()
		m.step = stepUninstall
		m.uninstallCursor = 0
		return m, nil

	case execDoneMsg:
		// Reload state from disk to reflect the last-used profile that just exited.
		state, _ := profiles.LoadState()
		m.state = state
		m.lastCombo = nil
		// Re-check installed CLIs in case something changed while the agent ran.
		m.installedProfiles = m.manager.InstalledProfiles()

		// Re-resolve the last-used combo from updated state.
		if state.LastProfileName != "" && state.LastBackendType != "" {
			for _, p := range m.installedProfiles {
				if p.Name() == state.LastProfileName {
					for _, b := range p.SupportedBackends() {
						if string(b.Type) == state.LastBackendType {
							combo := profiles.Combo{Profile: p, Backend: b}
							m.lastCombo = &combo
						}
					}
				}
			}
		}

		// Re-run preflight after agent exits.
		m.step = stepPreflight
		m.preflightChecking = true
		m.agentCursor = 0
		return m, runPreflight(m.apertureHost)

	case tea.KeyMsg:
		switch m.step {
		case stepPreflight:
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}

		case stepError:
			return m, tea.Quit

		case stepSelectAgent:
			return m.updateSelectAgent(msg)

		case stepSelectBackend:
			return m.updateSelectBackend(msg)

		case stepSettings:
			return m.updateSettings(msg)

		case stepEndpoints:
			return m.updateEndpoints(msg)

		case stepAddLocation:
			return m.updateAddLocation(msg)

		case stepAddLocationVertex:
			return m.updateAddLocationVertex(msg)

		case stepEnterVertexProject:
			return m.updateEnterVertexProject(msg)

		case stepInstall:
			return m.updateInstall(msg)

		case stepInstallAgents:
			return m.updateInstallAgents(msg)

		case stepUninstall:
			return m.updateUninstall(msg)

		case stepUninstallConfirm:
			return m.updateUninstallConfirm(msg)

		case stepCheckError:
			switch msg.String() {
			case "ctrl+c", "q":
				return m, tea.Quit
			case "esc":
				m.step = stepSelectBackend
			}
		}
	}
	return m, nil
}

// isInstalled reports whether a profile's binary is currently on PATH,
// using the cached installedProfiles slice.
func (m model) isInstalled(p profiles.Profile) bool {
	for _, ip := range m.installedProfiles {
		if ip.Name() == p.Name() {
			return true
		}
	}
	return false
}

// uninstalledProfiles returns profiles that are not currently installed.
func (m model) uninstalledProfiles() []profiles.Profile {
	var result []profiles.Profile
	for _, p := range m.allProfiles {
		if !m.isInstalled(p) {
			result = append(result, p)
		}
	}
	return result
}

func (m model) updateSelectAgent(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// rows: (last-used if exists) + installed profiles + (Install agents if uninstalled exist) + Settings
	hasLast := m.lastCombo != nil
	profileCount := len(m.installedProfiles)
	hasUninstalled := len(m.uninstalledProfiles()) > 0
	// +1 for the Settings row
	totalRows := profileCount + 1
	if hasLast {
		totalRows++
	}
	if hasUninstalled {
		totalRows++
	}

	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "s":
		m.step = stepSettings
		m.settingsCursor = 0
		return m, nil

	case "i":
		if hasUninstalled {
			m.step = stepInstallAgents
			m.installAgentsCursor = 0
			return m, nil
		}

	case "up", "k":
		if m.agentCursor > 0 {
			m.agentCursor--
		}

	case "down", "j":
		if m.agentCursor < totalRows-1 {
			m.agentCursor++
		}

	case "enter":
		return m.confirmAgentSelection()

	default:
		n, err := strconv.Atoi(msg.String())
		if err == nil {
			if hasLast && n == 0 {
				combo := *m.lastCombo
				return m.prepareAndExec(combo)
			}
			idx := n - 1
			if idx >= 0 && idx < profileCount {
				m.agentCursor = idx
				if hasLast {
					m.agentCursor = n
				}
				return m.confirmAgentSelection()
			}
		}
	}
	return m, nil
}

// confirmAgentSelection resolves which row was picked and transitions.
func (m model) confirmAgentSelection() (model, tea.Cmd) {
	hasLast := m.lastCombo != nil
	hasUninstalled := len(m.uninstalledProfiles()) > 0

	if hasLast && m.agentCursor == 0 {
		combo := *m.lastCombo
		return m.prepareAndExec(combo)
	}

	profileIdx := m.agentCursor
	if hasLast {
		profileIdx = m.agentCursor - 1
	}

	// Installed profile selected.
	if profileIdx >= 0 && profileIdx < len(m.installedProfiles) {
		chosen := m.installedProfiles[profileIdx]
		m.chosenProfile = chosen

		m.backendItems = m.manager.FilteredBackends(chosen, m.providers)
		if len(m.backendItems) == 0 {
			m.err = fmt.Sprintf("No compatible providers for %s.", chosen.Name())
			m.step = stepCheckError
			return m, nil
		}
		if len(m.backendItems) == 1 {
			b := m.backendItems[0]
			if checker, ok := chosen.(profiles.Checker); ok {
				if err := checker.Check(b); err != nil {
					m.err = err.Error()
					m.step = stepCheckError
					return m, nil
				}
			}
			combo := profiles.Combo{Profile: chosen, Backend: b}
			return m.prepareAndExec(combo)
		}
		m.backendCursor = 0
		m.step = stepSelectBackend
		return m, nil
	}

	// "Install agents" row (right after installed profiles).
	nextIdx := len(m.installedProfiles)
	if hasUninstalled && profileIdx == nextIdx {
		m.step = stepInstallAgents
		m.installAgentsCursor = 0
		return m, nil
	}
	if hasUninstalled {
		nextIdx++
	}

	// Settings row.
	if profileIdx == nextIdx {
		m.step = stepSettings
		m.settingsCursor = 0
		return m, nil
	}

	return m, nil
}

func (m model) updateInstall(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc", "n", "enter":
		m.step = stepSelectAgent
		return m, tea.ClearScreen
	case "y":
		return m, m.runInstall()
	}
	return m, nil
}

func (m model) updateInstallAgents(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	uninstalled := m.uninstalledProfiles()
	total := len(uninstalled)

	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		m.step = stepSelectAgent
		return m, tea.ClearScreen
	case "up", "k":
		if m.installAgentsCursor > 0 {
			m.installAgentsCursor--
		}
	case "down", "j":
		if m.installAgentsCursor < total-1 {
			m.installAgentsCursor++
		}
	case "enter":
		if m.installAgentsCursor < total {
			m.chosenProfile = uninstalled[m.installAgentsCursor]
			m.step = stepInstall
		}
		return m, nil
	default:
		n, err := strconv.Atoi(msg.String())
		if err == nil {
			idx := n - 1
			if idx >= 0 && idx < total {
				m.installAgentsCursor = idx
				m.chosenProfile = uninstalled[idx]
				m.step = stepInstall
			}
		}
	}
	return m, nil
}

func (m model) runInstall() tea.Cmd {
	inst, ok := m.chosenProfile.(profiles.Installer)
	if !ok {
		return nil
	}
	cmd := exec.Command("/bin/sh", "-c", inst.InstallHint())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return installDoneMsg{err: err}
	})
}

func (m model) updateUninstall(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	total := len(m.installedProfiles)

	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		m.step = stepSettings
		return m, nil
	case "up", "k":
		if m.uninstallCursor > 0 {
			m.uninstallCursor--
		}
	case "down", "j":
		if m.uninstallCursor < total-1 {
			m.uninstallCursor++
		}
	case "enter":
		if m.uninstallCursor < len(m.installedProfiles) {
			m.chosenProfile = m.installedProfiles[m.uninstallCursor]
			m.step = stepUninstallConfirm
		}
	default:
		n, err := strconv.Atoi(msg.String())
		if err == nil {
			idx := n - 1
			if idx >= 0 && idx < len(m.installedProfiles) {
				m.uninstallCursor = idx
				m.chosenProfile = m.installedProfiles[idx]
				m.step = stepUninstallConfirm
			}
		}
	}
	return m, nil
}

func (m model) updateUninstallConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc", "n", "enter":
		m.step = stepUninstall
	case "y":
		return m, m.runUninstall()
	}
	return m, nil
}

func (m model) runUninstall() tea.Cmd {
	uninst, ok := m.chosenProfile.(profiles.Uninstaller)
	if !ok {
		return nil
	}
	fn := uninst.Uninstall()
	return func() tea.Msg {
		return uninstallDoneMsg{err: fn()}
	}
}

func (m model) updateSelectBackend(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		m.step = stepSelectAgent
		return m, tea.ClearScreen

	case "up", "k":
		if m.backendCursor > 0 {
			m.backendCursor--
		}
	case "down", "j":
		if m.backendCursor < len(m.backendItems)-1 {
			m.backendCursor++
		}

	case "enter":
		return m.checkAndExecSelectedBackend()

	default:
		n, err := strconv.Atoi(msg.String())
		if err == nil {
			idx := n - 1
			if idx >= 0 && idx < len(m.backendItems) {
				m.backendCursor = idx
				return m.checkAndExecSelectedBackend()
			}
		}
	}
	return m, nil
}

// settingsRows returns the rows for the top-level settings menu.
// Row layout: "Aperture Endpoints" + "Uninstall" + "YOLO mode".
func (m model) settingsRows() []string {
	yoloLabel := "YOLO mode: off"
	if m.settings.YoloMode {
		yoloLabel = "YOLO mode: on"
	}
	return []string{"Aperture Endpoints", "Uninstall", yoloLabel}
}

// settingsYoloIdx returns the cursor index of the YOLO mode row.
func (m model) settingsYoloIdx() int { return 2 }

func (m model) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.settingsRows()
	total := len(rows)

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "esc", "q":
		m.step = stepSelectAgent
		return m, tea.ClearScreen

	case "up", "k":
		if m.settingsCursor > 0 {
			m.settingsCursor--
		}

	case "down", "j":
		if m.settingsCursor < total-1 {
			m.settingsCursor++
		}

	case "enter":
		return m.confirmSettingsSelection()

	default:
		n, err := strconv.Atoi(msg.String())
		if err == nil {
			idx := n - 1
			if idx >= 0 && idx < total {
				m.settingsCursor = idx
				return m.confirmSettingsSelection()
			}
		}
	}
	return m, nil
}

func (m model) confirmSettingsSelection() (model, tea.Cmd) {
	switch m.settingsCursor {
	case 0: // Aperture Endpoints
		m.step = stepEndpoints
		m.endpointsCursor = 0
		return m, nil
	case 1: // Uninstall
		m.step = stepUninstall
		m.uninstallCursor = 0
		return m, nil
	case m.settingsYoloIdx():
		m.settings.YoloMode = !m.settings.YoloMode
		_ = profiles.SaveSettings(m.settings)
		return m, nil
	}
	return m, nil
}

// endpointsRows returns the display rows for configured endpoints.
func (m model) endpointsRows() []string {
	rows := make([]string, 0, len(m.settings.Endpoints))
	for i, ep := range m.settings.Endpoints {
		label := ep.URL
		if i == 0 {
			label += " (active)"
		}
		if ep.VertexProjectID != "" {
			label += dimStyle.Render(" vertex:" + ep.VertexProjectID)
		}
		rows = append(rows, label)
	}
	return rows
}

func (m model) updateEndpoints(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	total := len(m.settings.Endpoints)

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "esc", "q":
		if m.endpointsFromSetup {
			return m, tea.Quit
		}
		m.step = stepSettings
		return m, nil

	case "a":
		m.step = stepAddLocation
		m.addLocationInput = ""
		return m, nil

	case "up", "k":
		if m.endpointsCursor > 0 {
			m.endpointsCursor--
		}

	case "down", "j":
		if m.endpointsCursor < total-1 {
			m.endpointsCursor++
		}

	case "enter":
		return m.confirmEndpointsSelection()

	case "d", "delete":
		if m.endpointsCursor < total && total > 1 {
			eps := make([]profiles.Endpoint, 0, total-1)
			eps = append(eps, m.settings.Endpoints[:m.endpointsCursor]...)
			eps = append(eps, m.settings.Endpoints[m.endpointsCursor+1:]...)
			m.settings.Endpoints = eps
			_ = profiles.SaveSettings(m.settings)
			if m.endpointsCursor >= len(m.settings.Endpoints) {
				m.endpointsCursor = len(m.settings.Endpoints) - 1
			}
			if m.apertureHost != m.settings.Endpoints[0].URL {
				m.apertureHost = m.settings.Endpoints[0].URL
			}
		}

	default:
		n, err := strconv.Atoi(msg.String())
		if err == nil {
			idx := n - 1
			if idx >= 0 && idx < total {
				m.endpointsCursor = idx
				return m.confirmEndpointsSelection()
			}
		}
	}
	return m, nil
}

func (m model) confirmEndpointsSelection() (model, tea.Cmd) {
	if m.endpointsCursor < len(m.settings.Endpoints) {
		selected := m.settings.Endpoints[m.endpointsCursor]
		eps := []profiles.Endpoint{selected}
		for i, ep := range m.settings.Endpoints {
			if i != m.endpointsCursor {
				eps = append(eps, ep)
			}
		}
		m.settings.Endpoints = eps
		_ = profiles.SaveSettings(m.settings)
		m.apertureHost = selected.URL
		m.step = stepPreflight
		m.preflightChecking = true
		m.preflightErr = ""
		return m, runPreflight(selected.URL)
	}
	return m, nil
}

func (m model) updateAddLocation(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.step = stepEndpoints
		return m, nil
	case "enter":
		loc := strings.TrimSpace(m.addLocationInput)
		if loc == "" {
			return m, nil
		}
		m.pendingLocationURL = loc
		m.vertexProjectInput = ""
		m.step = stepAddLocationVertex
		return m, nil
	case "backspace":
		if len(m.addLocationInput) > 0 {
			m.addLocationInput = m.addLocationInput[:len(m.addLocationInput)-1]
		}
	default:
		if len(msg.String()) == 1 {
			m.addLocationInput += msg.String()
		}
	}
	return m, nil
}

// upsertLocation ensures loc is in settings.Endpoints without duplicates.
// If it already exists it stays in place; otherwise it is appended.
func upsertLocation(s profiles.Settings, loc string) profiles.Settings {
	for _, ep := range s.Endpoints {
		if ep.URL == loc {
			return s
		}
	}
	s.Endpoints = append(s.Endpoints, profiles.Endpoint{URL: loc})
	return s
}

func (m model) updateAddLocationVertex(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.settings = upsertEndpointVertex(m.settings, m.pendingLocationURL, "")
		_ = profiles.SaveSettings(m.settings)
		m.step = stepEndpoints
		m.endpointsCursor = 0
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.vertexProjectInput)
		m.settings = upsertEndpointVertex(m.settings, m.pendingLocationURL, val)
		_ = profiles.SaveSettings(m.settings)
		m.step = stepEndpoints
		m.endpointsCursor = 0
		return m, nil
	case "backspace":
		if len(m.vertexProjectInput) > 0 {
			m.vertexProjectInput = m.vertexProjectInput[:len(m.vertexProjectInput)-1]
		}
	default:
		if len(msg.String()) == 1 {
			m.vertexProjectInput += msg.String()
		}
	}
	return m, nil
}

// upsertEndpointVertex ensures an endpoint with the given URL is in
// settings.Endpoints. If the URL exists, its VertexProjectID is updated
// in place; otherwise a new Endpoint is appended.
func upsertEndpointVertex(s profiles.Settings, url, vertexProjectID string) profiles.Settings {
	for i, ep := range s.Endpoints {
		if ep.URL == url {
			s.Endpoints[i].VertexProjectID = vertexProjectID
			return s
		}
	}
	s.Endpoints = append(s.Endpoints, profiles.Endpoint{URL: url, VertexProjectID: vertexProjectID})
	return s
}

func (m model) checkAndExecSelectedBackend() (model, tea.Cmd) {
	if m.backendCursor < 0 || m.backendCursor >= len(m.backendItems) {
		return m, nil
	}
	b := m.backendItems[m.backendCursor]
	if checker, ok := m.chosenProfile.(profiles.Checker); ok {
		if err := checker.Check(b); err != nil {
			m.err = err.Error()
			m.step = stepCheckError
			return m, nil
		}
	}
	combo := profiles.Combo{Profile: m.chosenProfile, Backend: b}
	return m.prepareAndExec(combo)
}

// prepareAndExec intercepts a launch to prompt for the Vertex project ID
// when needed. If the combo uses a Vertex backend and the profile implements
// VertexConfigurer, it either injects the saved project ID or transitions
// to the text-input step. For non-Vertex combos or profiles that don't
// implement VertexConfigurer (e.g. Gemini CLI) it proceeds directly.
func (m model) prepareAndExec(combo profiles.Combo) (model, tea.Cmd) {
	vc, ok := combo.Profile.(profiles.VertexConfigurer)
	if !ok || combo.Backend.Type != profiles.BackendVertex {
		return m, m.execCombo(combo)
	}
	if len(m.settings.Endpoints) > 0 && m.settings.Endpoints[0].VertexProjectID != "" {
		vc.SetVertexProjectID(m.settings.Endpoints[0].VertexProjectID)
		return m, m.execCombo(combo)
	}
	// Need to ask.
	m.pendingCombo = &combo
	m.vertexProjectInput = ""
	m.step = stepEnterVertexProject
	return m, nil
}

func (m model) updateEnterVertexProject(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.pendingCombo != nil {
			// Came from exec flow, go back to backend selection.
			m.pendingCombo = nil
			m.step = stepSelectBackend
		} else {
			m.step = stepSelectAgent
		}
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.vertexProjectInput)
		if val == "" {
			return m, nil
		}
		if len(m.settings.Endpoints) > 0 {
			m.settings.Endpoints[0].VertexProjectID = val
		}
		_ = profiles.SaveSettings(m.settings)
		if m.pendingCombo != nil {
			combo := *m.pendingCombo
			m.pendingCombo = nil
			if vc, ok := combo.Profile.(profiles.VertexConfigurer); ok {
				vc.SetVertexProjectID(val)
			}
			return m, m.execCombo(combo)
		}
		// Came from settings, return there.
		m.step = stepSettings
		return m, nil
	case "backspace":
		if len(m.vertexProjectInput) > 0 {
			m.vertexProjectInput = m.vertexProjectInput[:len(m.vertexProjectInput)-1]
		}
	default:
		if len(msg.String()) == 1 {
			m.vertexProjectInput += msg.String()
		}
	}
	return m, nil
}

func (m model) execCombo(combo profiles.Combo) tea.Cmd {
	env, err := combo.Profile.Env(m.apertureHost, combo.Backend)
	if err != nil {
		return tea.Quit
	}

	if ps, ok := combo.Profile.(profiles.ProviderEnvSetter); ok {
		for k, v := range ps.ProviderEnv(combo.Backend, m.providers) {
			env[k] = v
		}
	}

	binary := profiles.FindBinary(combo.Profile)
	if binary == "" {
		binary = combo.Profile.BinaryName()
	}

	_ = profiles.SaveState(profiles.StateFile{
		LastProfileName: combo.Profile.Name(),
		LastBackendType: string(combo.Backend.Type),
	})

	envPairs := os.Environ()
	for k, v := range env {
		envPairs = append(envPairs, k+"="+v)
	}

	var configCleanup func()
	var configEnvKey, configPath string
	if cw, ok := combo.Profile.(profiles.ConfigWriter); ok {
		var err error
		configEnvKey, configPath, configCleanup, err = cw.WriteConfig(m.apertureHost, combo.Backend)
		if err != nil {
			return tea.Quit
		}
		if configEnvKey != "" && configPath != "" {
			envPairs = append(envPairs, configEnvKey+"="+configPath)
		}
	}

	if m.debug {
		fmt.Fprintf(os.Stderr, "\r\n[debug] launching %s with env:\r\n", binary)
		for k, v := range env {
			fmt.Fprintf(os.Stderr, "[debug]   %s=%s\r\n", k, v)
		}
		if configEnvKey != "" && configPath != "" {
			fmt.Fprintf(os.Stderr, "[debug]   %s=%s\r\n", configEnvKey, configPath)
		}
	}

	var extraArgs []string
	if m.settings.YoloMode {
		if yp, ok := combo.Profile.(profiles.YoloProfile); ok {
			extraArgs = yp.YoloArgs()
		}
	}

	if m.debug && len(extraArgs) > 0 {
		fmt.Fprintf(os.Stderr, "[debug]   args: %v\r\n", extraArgs)
	}

	cmd := exec.Command(binary, extraArgs...)
	cmd.Env = envPairs
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if configCleanup != nil {
			configCleanup()
		}
		return execDoneMsg{err: err}
	})
}

func (m model) View() string {
	var sb strings.Builder

	switch m.step {
	case stepPreflight:
		sb.WriteString(dotYellow + " Checking " + m.apertureHost + " …\n")

	case stepCheckError:
		sb.WriteString(errorStyle.Render("Cannot launch"))
		sb.WriteString("\n\n")
		sb.WriteString(m.err)
		sb.WriteString("\n\n")
		sb.WriteString(dimStyle.Render("Esc to go back · q to quit\n"))

	case stepError:
		sb.WriteString(errorStyle.Render("Error: " + m.err))
		sb.WriteString("\n\nPress any key to exit.\n")

	case stepSelectAgent:
		sb.WriteString(dotGreen + " Connected to " + m.apertureHost)
		if len(m.providers) > 0 {
			sb.WriteString(fmt.Sprintf(" (%d providers)", len(m.providers)))
		}
		sb.WriteString("\n\n")
		sb.WriteString(titleStyle.Render("Which editor do you want to use?"))
		sb.WriteString("\n")

		hasLast := m.lastCombo != nil
		hasUninstalled := len(m.uninstalledProfiles()) > 0

		if hasLast {
			label := fmt.Sprintf("  [0] Last Used: %s - %s",
				m.lastCombo.Profile.Name(), m.lastCombo.Backend.DisplayName)
			if m.agentCursor == 0 {
				sb.WriteString(selectedStyle.Render(label))
			} else {
				sb.WriteString(label)
			}
			sb.WriteString("\n")
		}

		for i, p := range m.installedProfiles {
			n := i + 1
			cursor := i
			if hasLast {
				cursor = i + 1
			}
			backends := m.manager.FilteredBackends(p, m.providers)
			var label string
			if len(backends) == 1 {
				label = fmt.Sprintf("  [%d] %s - %s", n, p.Name(), backends[0].DisplayName)
			} else {
				label = fmt.Sprintf("  [%d] %s", n, p.Name())
			}
			if m.agentCursor == cursor {
				sb.WriteString(selectedStyle.Render(label))
			} else {
				sb.WriteString(label)
			}
			sb.WriteString("\n")
		}

		sb.WriteString("\n")

		// "Install agents" row — only if uninstalled profiles exist
		nextCursor := len(m.installedProfiles)
		if hasLast {
			nextCursor++
		}
		if hasUninstalled {
			installLabel := "  [i] Install agents"
			if m.agentCursor == nextCursor {
				sb.WriteString(selectedStyle.Render(installLabel))
			} else {
				sb.WriteString(installLabel)
			}
			sb.WriteString("\n")
			nextCursor++
		}

		// Settings row
		settingsLabel := "  [s] Settings"
		if m.agentCursor == nextCursor {
			sb.WriteString(selectedStyle.Render(settingsLabel))
		} else {
			sb.WriteString(settingsLabel)
		}
		sb.WriteString("\n")
		sb.WriteString("  [q] Quit")
		sb.WriteString("\n")

		sb.WriteString("\n")
		if hasLast {
			sb.WriteString(dimStyle.Render("Selection (default: 0): "))
		} else {
			sb.WriteString(dimStyle.Render("Selection: "))
		}

	case stepInstallAgents:
		sb.WriteString(titleStyle.Render("Install agents"))
		sb.WriteString("\n")
		uninstalled := m.uninstalledProfiles()
		if len(uninstalled) == 0 {
			sb.WriteString(dimStyle.Render("  All agents are installed.\n"))
		} else {
			for i, p := range uninstalled {
				label := fmt.Sprintf("  [%d] %s", i+1, p.Name())
				if m.installAgentsCursor == i {
					sb.WriteString(selectedStyle.Render(label))
				} else {
					sb.WriteString(label)
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Enter to select · Esc to go back\n"))

	case stepSelectBackend:
		sb.WriteString(titleStyle.Render("Choose a Provider:"))
		sb.WriteString("\n")

		for i, b := range m.backendItems {
			label := fmt.Sprintf("  [%d] %s", i+1, b.DisplayName)
			if m.backendCursor == i {
				sb.WriteString(selectedStyle.Render(label))
			} else {
				sb.WriteString(label)
			}
			sb.WriteString("\n")
		}

		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Selection: "))

	case stepSettings:
		sb.WriteString(titleStyle.Render("Settings"))
		sb.WriteString("\n")
		for i, row := range m.settingsRows() {
			var renderedRow string
			if i == m.settingsYoloIdx() && m.settings.YoloMode {
				renderedRow = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true).Render(row)
			} else {
				renderedRow = row
			}
			label := fmt.Sprintf("  [%d] %s", i+1, renderedRow)
			if m.settingsCursor == i {
				sb.WriteString(selectedStyle.Render(label))
			} else {
				sb.WriteString(label)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Enter to select · Esc to go back\n"))

	case stepEndpoints:
		if m.endpointsFromSetup {
			sb.WriteString(dotRed + " Could not reach " + m.apertureHost + "\n")
			if m.preflightErr != "" {
				sb.WriteString(dimStyle.Render("  "+m.preflightErr) + "\n")
			}
			sb.WriteString("\n")
		}
		sb.WriteString(titleStyle.Render("Aperture Endpoints"))
		sb.WriteString("\n")
		rows := m.endpointsRows()
		for i, row := range rows {
			if i == 0 {
				row = greenStyle.Render(row)
			}
			label := fmt.Sprintf("  [%d] %s", i+1, row)
			if m.endpointsCursor == i {
				sb.WriteString(selectedStyle.Render(label))
			} else {
				sb.WriteString(label)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString("  [a] Add endpoint\n")
		sb.WriteString("\n")
		escHint := "Esc to go back"
		if m.endpointsFromSetup {
			escHint = "Esc to quit"
		}
		sb.WriteString(dimStyle.Render("Enter to select · d to remove · a to add · " + escHint + "\n"))

	case stepInstall:
		sb.WriteString(titleStyle.Render("Install " + m.chosenProfile.Name() + "?"))
		sb.WriteString("\n")
		if inst, ok := m.chosenProfile.(profiles.Installer); ok {
			sb.WriteString("  This will run: " + inst.InstallHint() + "\n")
		}
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("y to install · Enter/Esc to cancel\n"))

	case stepUninstall:
		sb.WriteString(titleStyle.Render("Uninstall"))
		sb.WriteString("\n")
		if len(m.installedProfiles) == 0 {
			sb.WriteString(dimStyle.Render("  No agents installed.\n"))
		} else {
			for i, p := range m.installedProfiles {
				label := fmt.Sprintf("  [%d] %s", i+1, p.Name())
				if m.uninstallCursor == i {
					sb.WriteString(selectedStyle.Render(label))
				} else {
					sb.WriteString(label)
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Enter to select · Esc to go back\n"))

	case stepUninstallConfirm:
		sb.WriteString(titleStyle.Render("Uninstall " + m.chosenProfile.Name() + "?"))
		sb.WriteString("\n")
		if uninst, ok := m.chosenProfile.(profiles.Uninstaller); ok {
			sb.WriteString("  This will run: " + uninst.UninstallHint() + "\n")
		}
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("y to uninstall · Enter/Esc to cancel\n"))

	case stepAddLocation:
		sb.WriteString(titleStyle.Render("Add Endpoint:"))
		sb.WriteString("\n")
		sb.WriteString("  > " + m.addLocationInput + "█\n")
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Press Enter to save, Esc to cancel.\n"))

	case stepAddLocationVertex:
		sb.WriteString(titleStyle.Render("Vertex Project ID (optional):"))
		sb.WriteString("\n")
		sb.WriteString("  > " + m.vertexProjectInput + "█\n")
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Press Enter to save, Esc to skip.\n"))

	case stepEnterVertexProject:
		sb.WriteString(titleStyle.Render("Vertex Project ID:"))
		sb.WriteString("\n")
		sb.WriteString("  > " + m.vertexProjectInput + "█\n")
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Press Enter to save, Esc to cancel.\n"))
	}

	return sb.String()
}
