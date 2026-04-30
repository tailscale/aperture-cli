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
	stepPreflight        step = iota // checking /api/providers
	stepSelectProfile                // choose profile
	stepSelectProvider               // choose provider for the selected profile
	stepSelectBackend                // choose backend (only when genuinely different compat keys)
	stepSelectModel                  // choose default model from provider's model list
	stepSettings                     // top-level settings menu
	stepEndpoints                    // manage aperture endpoints
	stepAddLocation                  // type a new endpoint URL
	stepInstall                      // show install hint for an uninstalled profile
	stepInstallAgents                // choose an uninstalled profile to install
	stepUninstall                    // list installed profiles to uninstall
	stepUninstallConfirm             // confirm uninstall for a chosen profile
	stepCheckError                   // pre-launch validation failure
	stepError                        // fatal error
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

type resolvedSelection struct {
	combo         profiles.Combo
	provider      profiles.ProviderInfo
	selectedModel string
}

type model struct {
	apertureHost string
	settings     profiles.Settings
	state        profiles.StateFile
	manager      *profiles.Manager

	// resolved selection for the last-used shortcut
	lastSelection *resolvedSelection

	// all known profiles; installedProfiles is the subset on PATH
	allProfiles       []profiles.Profile
	installedProfiles []profiles.Profile

	step          step
	profileCursor int
	backendItems  []profiles.Backend
	backendCursor int

	chosenProfile  profiles.Profile
	chosenProvider profiles.ProviderInfo
	chosenBackend  profiles.Backend

	// provider selection step
	providerItems  []profiles.ProviderInfo
	providerCursor int

	// model selection step
	modelItems    []string
	modelCursor   int
	selectedModel string

	// preflight state
	preflightChecking bool
	providers         []profiles.ProviderInfo
	preflightErr      string

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

	buildVersion string
	debug        bool
	err          string
}

// NewModel constructs the TUI model. It satisfies tea.Model.
func NewModel(apertureHost string, settings profiles.Settings, state profiles.StateFile, buildVersion string, debug bool) tea.Model {
	mgr := profiles.NewManager()

	m := model{
		apertureHost:      apertureHost,
		settings:          settings,
		state:             state,
		manager:           mgr,
		allProfiles:       mgr.AllProfiles(),
		installedProfiles: mgr.InstalledProfiles(),
		buildVersion:      buildVersion,
		debug:             debug,
		step:              stepPreflight,
		preflightChecking: true,
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

type autoSelectMsg struct{ selection resolvedSelection }
type execDoneMsg struct{ err error }
type launchDoneMsg struct{ err error }
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
		m.refreshLastSelection()
		m.resetProfileCursor()
		// Auto-select only when there's a single unambiguous path through
		// profile → provider → backend.
		if selection, ok := m.tryAutoSelect(); ok {
			return m, func() tea.Msg { return autoSelectMsg{selection: selection} }
		}
		m.step = stepSelectProfile
		return m, tea.ClearScreen

	case autoSelectMsg:
		return m, m.execSelection(msg.selection)

	case installDoneMsg:
		// Re-check installed CLIs after the install command finishes.
		m.installedProfiles = m.manager.InstalledProfiles()
		m.step = stepSelectProfile
		m.refreshLastSelection()
		m.resetProfileCursor()
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
		m.lastSelection = nil
		// Re-check installed CLIs in case something changed while the agent ran.
		m.installedProfiles = m.manager.InstalledProfiles()

		// Re-run preflight after agent exits.
		m.step = stepPreflight
		m.preflightChecking = true
		m.profileCursor = 0
		return m, runPreflight(m.apertureHost)

	case launchDoneMsg:
		// Desktop app launched (returns immediately). Go back to the profile
		// selection screen without re-running preflight to avoid an
		// auto-select loop.
		if msg.err != nil {
			m.err = msg.err.Error()
			m.step = stepError
			return m, nil
		}
		state, _ := profiles.LoadState()
		m.state = state
		m.installedProfiles = m.manager.InstalledProfiles()
		m.refreshLastSelection()
		m.step = stepSelectProfile
		m.resetProfileCursor()
		return m, tea.ClearScreen

	case tea.KeyMsg:
		switch m.step {
		case stepPreflight:
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}

		case stepError:
			return m, tea.Quit

		case stepSelectProfile:
			return m.updateSelectProfile(msg)

		case stepSelectProvider:
			return m.updateSelectProvider(msg)

		case stepSelectBackend:
			return m.updateSelectBackend(msg)

		case stepSelectModel:
			return m.updateSelectModel(msg)

		case stepSettings:
			return m.updateSettings(msg)

		case stepEndpoints:
			return m.updateEndpoints(msg)

		case stepAddLocation:
			return m.updateAddLocation(msg)

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
				m.step = stepSelectProvider
			}
		}
	}
	return m, nil
}

// tryAutoSelect returns a resolved selection and true when there is exactly one installed
// profile, one compatible provider, and one deduped backend for that provider,
// and either the provider has 0-1 models or the profile doesn't support model
// selection. This is the only case where we skip the menu entirely.
func (m model) tryAutoSelect() (resolvedSelection, bool) {
	if len(m.installedProfiles) != 1 {
		return resolvedSelection{}, false
	}
	p := m.installedProfiles[0]
	providers := m.manager.CompatibleProviders(p, m.providers)
	if len(providers) != 1 {
		return resolvedSelection{}, false
	}
	backends := m.manager.BackendsForProvider(p, providers[0])
	backends = m.manager.DedupBackends(p, backends)
	if len(backends) != 1 {
		return resolvedSelection{}, false
	}
	// If the profile supports model selection and the provider has multiple
	// models, don't auto-select — the user needs to pick a model.
	if _, ok := p.(profiles.ModelSelector); ok && len(providers[0].Models) > 1 {
		return resolvedSelection{}, false
	}
	selectedModel := ""
	if len(providers[0].Models) == 1 {
		selectedModel = providers[0].ID + "/" + providers[0].Models[0]
	}
	return resolvedSelection{
		combo:         profiles.Combo{Profile: p, Backend: backends[0]},
		provider:      providers[0],
		selectedModel: selectedModel,
	}, true
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

func (m *model) resetProfileCursor() {
	if m.lastSelection != nil {
		m.profileCursor = -1
		return
	}
	m.profileCursor = 0
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

func (m model) updateSelectProfile(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	profileCount := len(m.installedProfiles)
	minCursor := 0
	if m.lastSelection != nil {
		minCursor = -1
	}

	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit

	case "s":
		m.step = stepSettings
		m.settingsCursor = 0
		return m, nil

	case "i":
		if len(m.uninstalledProfiles()) > 0 {
			m.step = stepInstallAgents
			m.installAgentsCursor = 0
			return m, nil
		}

	case "up", "k":
		if m.profileCursor > minCursor {
			m.profileCursor--
		}

	case "down", "j":
		if m.profileCursor < profileCount-1 {
			m.profileCursor++
		}

	case "enter":
		if m.profileCursor == -1 && m.lastSelection != nil {
			return m, m.execSelection(*m.lastSelection)
		}
		return m.confirmProfileSelection()

	default:
		n, err := strconv.Atoi(msg.String())
		if err == nil {
			// [0] launches the last-used profile/provider/model selection.
			if n == 0 && m.lastSelection != nil {
				return m, m.execSelection(*m.lastSelection)
			}
			// [1..N] selects a profile directly.
			idx := n - 1
			if idx >= 0 && idx < profileCount {
				m.profileCursor = idx
				return m.confirmProfileSelection()
			}
		}
	}
	return m, nil
}

// confirmProfileSelection resolves the chosen profile and transitions to
// provider selection or auto-launches if only one provider is compatible.
func (m model) confirmProfileSelection() (model, tea.Cmd) {
	if m.profileCursor < 0 || m.profileCursor >= len(m.installedProfiles) {
		return m, nil
	}
	chosen := m.installedProfiles[m.profileCursor]
	m.chosenProfile = chosen
	m.selectedModel = ""

	m.providerItems = m.manager.CompatibleProviders(chosen, m.providers)
	if len(m.providerItems) == 0 {
		m.err = fmt.Sprintf("No compatible providers for %s.", chosen.Name())
		m.step = stepCheckError
		return m, nil
	}
	if len(m.providerItems) == 1 {
		m.chosenProvider = m.providerItems[0]
		return m.resolveProviderAndExec()
	}
	m.providerCursor = 0
	m.step = stepSelectProvider
	return m, nil
}

func (m model) updateSelectProvider(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		m.step = stepSelectProfile
		return m, tea.ClearScreen
	case "up", "k":
		if m.providerCursor > 0 {
			m.providerCursor--
		}
	case "down", "j":
		if m.providerCursor < len(m.providerItems)-1 {
			m.providerCursor++
		}
	case "enter":
		return m.confirmProviderSelection()
	default:
		n, err := strconv.Atoi(msg.String())
		if err == nil {
			idx := n - 1
			if idx >= 0 && idx < len(m.providerItems) {
				m.providerCursor = idx
				return m.confirmProviderSelection()
			}
		}
	}
	return m, nil
}

// confirmProviderSelection resolves the chosen provider and either auto-launches
// (single backend) or shows the backend submenu.
func (m model) confirmProviderSelection() (model, tea.Cmd) {
	if m.providerCursor < 0 || m.providerCursor >= len(m.providerItems) {
		return m, nil
	}
	m.chosenProvider = m.providerItems[m.providerCursor]
	return m.resolveProviderAndExec()
}

// resolveProviderAndExec checks how many backends the chosen provider supports
// for the chosen profile, then either auto-launches, shows a model picker, or
// shows the backend submenu.
func (m model) resolveProviderAndExec() (model, tea.Cmd) {
	backends := m.manager.BackendsForProvider(m.chosenProfile, m.chosenProvider)
	if len(backends) == 0 {
		m.err = fmt.Sprintf("No compatible backends for %s with %s.",
			m.chosenProfile.Name(), m.chosenProvider.DisplayName())
		m.step = stepCheckError
		return m, nil
	}

	// Deduplicate backends that share the same compat key signature
	// (e.g. Anthropic and ZAI both use "anthropic_messages").
	backends = m.manager.DedupBackends(m.chosenProfile, backends)

	if len(backends) == 1 {
		return m.proceedWithBackend(backends[0])
	}
	// Multiple genuinely-different backends (e.g. Anthropic vs Bedrock).
	m.backendItems = backends
	m.backendCursor = 0
	m.step = stepSelectBackend
	return m, nil
}

// proceedWithBackend resolves the model selection for a single backend and
// either auto-launches or shows the model picker.
func (m model) proceedWithBackend(b profiles.Backend) (model, tea.Cmd) {
	_, wantsModel := m.chosenProfile.(profiles.ModelSelector)

	if wantsModel && len(m.chosenProvider.Models) > 1 {
		m.chosenBackend = b
		m.modelItems = fqnModels(m.chosenProvider)
		m.modelCursor = 0
		m.step = stepSelectModel
		return m, nil
	}

	// Auto-select the single model if available.
	if len(m.chosenProvider.Models) == 1 {
		m.selectedModel = m.chosenProvider.ID + "/" + m.chosenProvider.Models[0]
	}

	if checker, ok := m.chosenProfile.(profiles.Checker); ok {
		if err := checker.Check(b); err != nil {
			m.err = err.Error()
			m.step = stepCheckError
			return m, nil
		}
	}
	combo := profiles.Combo{Profile: m.chosenProfile, Backend: b}
	return m, m.execCombo(combo)
}

func (m model) updateInstall(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc", "n", "enter":
		m.step = stepSelectProfile
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
		m.step = stepSelectProfile
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
	// Host-aware installers write platform config and return a download command.
	if hai, ok := m.chosenProfile.(profiles.HostAwareInstaller); ok {
		cmd, err := hai.RunInstall(m.apertureHost)
		if err != nil {
			return func() tea.Msg { return installDoneMsg{err: err} }
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return installDoneMsg{err: err}
		})
	}

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
		m.step = stepSelectProvider
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

func (m model) updateSelectModel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		m.step = stepSelectProvider
		return m, tea.ClearScreen
	case "up", "k":
		if m.modelCursor > 0 {
			m.modelCursor--
		}
	case "down", "j":
		if m.modelCursor < len(m.modelItems)-1 {
			m.modelCursor++
		}
	case "enter":
		return m.confirmModelSelection()
	default:
		n, err := strconv.Atoi(msg.String())
		if err == nil {
			idx := n - 1
			if idx >= 0 && idx < len(m.modelItems) {
				m.modelCursor = idx
				return m.confirmModelSelection()
			}
		}
	}
	return m, nil
}

func (m model) confirmModelSelection() (model, tea.Cmd) {
	if m.modelCursor < 0 || m.modelCursor >= len(m.modelItems) {
		return m, nil
	}
	m.selectedModel = m.modelItems[m.modelCursor]

	b := m.chosenBackend
	if checker, ok := m.chosenProfile.(profiles.Checker); ok {
		if err := checker.Check(b); err != nil {
			m.err = err.Error()
			m.step = stepCheckError
			return m, nil
		}
	}
	combo := profiles.Combo{Profile: m.chosenProfile, Backend: b}
	return m, m.execCombo(combo)
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
		m.step = stepSelectProfile
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
		m.settings = upsertLocation(m.settings, loc)
		_ = profiles.SaveSettings(m.settings)
		m.step = stepEndpoints
		m.endpointsCursor = 0
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
// fqnModels returns fully qualified model names in the form "provider_id/model_id".
func fqnModels(p profiles.ProviderInfo) []string {
	out := make([]string, len(p.Models))
	for i, m := range p.Models {
		out[i] = p.ID + "/" + m
	}
	return out
}

func containsString(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}

func (m *model) refreshLastSelection() {
	m.lastSelection = nil

	if m.state.LastProfileName == "" || m.state.LastBackendType == "" {
		return
	}

	var selectedProfile profiles.Profile
	var selectedBackend profiles.Backend
	for _, p := range m.installedProfiles {
		if p.Name() != m.state.LastProfileName {
			continue
		}
		for _, b := range p.SupportedBackends() {
			if string(b.Type) == m.state.LastBackendType {
				selectedProfile = p
				selectedBackend = b
				break
			}
		}
		break
	}
	if selectedProfile == nil {
		return
	}

	provider, ok := m.resolveLastProvider(selectedProfile, selectedBackend)
	if !ok {
		return
	}

	selectedModel, ok := m.resolveLastModel(selectedProfile, provider)
	if !ok {
		return
	}

	m.lastSelection = &resolvedSelection{
		combo: profiles.Combo{
			Profile: selectedProfile,
			Backend: selectedBackend,
		},
		provider:      provider,
		selectedModel: selectedModel,
	}
}

func (m model) resolveLastProvider(p profiles.Profile, b profiles.Backend) (profiles.ProviderInfo, bool) {
	var candidates []profiles.ProviderInfo
	for _, provider := range m.manager.CompatibleProviders(p, m.providers) {
		for _, supportedBackend := range m.manager.BackendsForProvider(p, provider) {
			if supportedBackend.Type == b.Type {
				candidates = append(candidates, provider)
				break
			}
		}
	}
	if len(candidates) == 0 {
		return profiles.ProviderInfo{}, false
	}

	if m.state.LastProviderID != "" {
		for _, provider := range candidates {
			if provider.ID == m.state.LastProviderID {
				return provider, true
			}
		}
		return profiles.ProviderInfo{}, false
	}

	if len(candidates) == 1 {
		return candidates[0], true
	}
	return profiles.ProviderInfo{}, false
}

func (m model) resolveLastModel(p profiles.Profile, provider profiles.ProviderInfo) (string, bool) {
	if _, ok := p.(profiles.ModelSelector); !ok {
		return "", true
	}

	models := fqnModels(provider)
	if len(models) == 0 {
		return "", true
	}

	if m.state.LastModel != "" && containsString(models, m.state.LastModel) {
		return m.state.LastModel, true
	}

	if len(models) == 1 {
		return models[0], true
	}

	return "", false
}

func upsertLocation(s profiles.Settings, loc string) profiles.Settings {
	for _, ep := range s.Endpoints {
		if ep.URL == loc {
			return s
		}
	}
	s.Endpoints = append(s.Endpoints, profiles.Endpoint{URL: loc})
	return s
}

func (m model) checkAndExecSelectedBackend() (model, tea.Cmd) {
	if m.backendCursor < 0 || m.backendCursor >= len(m.backendItems) {
		return m, nil
	}
	b := m.backendItems[m.backendCursor]

	// If the profile supports model selection and the provider has multiple
	// models, show the model picker instead of launching immediately.
	_, wantsModel := m.chosenProfile.(profiles.ModelSelector)
	if wantsModel && len(m.chosenProvider.Models) > 1 {
		m.chosenBackend = b
		m.modelItems = fqnModels(m.chosenProvider)
		m.modelCursor = 0
		m.step = stepSelectModel
		return m, nil
	}

	if checker, ok := m.chosenProfile.(profiles.Checker); ok {
		if err := checker.Check(b); err != nil {
			m.err = err.Error()
			m.step = stepCheckError
			return m, nil
		}
	}
	combo := profiles.Combo{Profile: m.chosenProfile, Backend: b}
	return m, m.execCombo(combo)
}

func (m model) execSelection(selection resolvedSelection) tea.Cmd {
	m.chosenProvider = selection.provider
	m.selectedModel = selection.selectedModel
	return m.execCombo(selection.combo)
}

func (m model) execCombo(combo profiles.Combo) tea.Cmd {
	// Desktop app profiles update config if needed and launch the app.
	// The launch returns immediately (unlike CLI profiles which block).
	if launcher, ok := combo.Profile.(profiles.Launcher); ok {
		_ = profiles.SaveState(profiles.StateFile{
			LastProfileName: combo.Profile.Name(),
			LastBackendType: string(combo.Backend.Type),
			LastProviderID:  m.chosenProvider.ID,
			LastModel:       m.selectedModel,
		})
		host := m.apertureHost
		return func() tea.Msg {
			return launchDoneMsg{err: launcher.Launch(host)}
		}
	}

	env, err := combo.Profile.Env(m.apertureHost, combo.Backend)
	if err != nil {
		return tea.Quit
	}

	if ps, ok := combo.Profile.(profiles.ProviderEnvSetter); ok {
		for k, v := range ps.ProviderEnv(combo.Backend, []profiles.ProviderInfo{m.chosenProvider}) {
			env[k] = v
		}
	}

	if m.selectedModel != "" {
		if ms, ok := combo.Profile.(profiles.ModelSelector); ok {
			ms.ApplyModel(m.selectedModel, env)
		}
	}

	binary := profiles.FindBinary(combo.Profile)
	if binary == "" {
		binary = combo.Profile.BinaryName()
	}

	_ = profiles.SaveState(profiles.StateFile{
		LastProfileName: combo.Profile.Name(),
		LastBackendType: string(combo.Backend.Type),
		LastProviderID:  m.chosenProvider.ID,
		LastModel:       m.selectedModel,
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
	if m.selectedModel != "" {
		if ma, ok := combo.Profile.(profiles.ModelArgSelector); ok {
			extraArgs = append(extraArgs, ma.ModelArgs(m.selectedModel)...)
		}
	}
	if m.settings.YoloMode {
		if yp, ok := combo.Profile.(profiles.YoloProfile); ok {
			extraArgs = append(extraArgs, yp.YoloArgs()...)
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

	case stepSelectProfile:
		sb.WriteString(dotGreen + " Connected to " + m.apertureHost)
		if len(m.providers) > 0 {
			sb.WriteString(fmt.Sprintf(" (%d providers)", len(m.providers)))
		}
		sb.WriteString("\n\n")
		sb.WriteString(titleStyle.Render("Which editor do you want to use?"))
		sb.WriteString("\n")

		if m.lastSelection != nil {
			label := fmt.Sprintf("  [0] Quick select: %s via %s - %s",
				m.lastSelection.combo.Profile.Name(),
				m.lastSelection.provider.DisplayName(),
				m.lastSelection.combo.Backend.DisplayName)
			if m.lastSelection.selectedModel != "" {
				label += " - " + m.lastSelection.selectedModel
			}
			if m.profileCursor == -1 {
				sb.WriteString(selectedStyle.Render(label))
			} else {
				sb.WriteString(label)
			}
			sb.WriteString("\n")
			sb.WriteString("\n")
		}

		for i, p := range m.installedProfiles {
			n := i + 1
			label := fmt.Sprintf("  [%d] %s", n, p.Name())
			if m.profileCursor == i {
				sb.WriteString(selectedStyle.Render(label))
			} else {
				sb.WriteString(label)
			}
			sb.WriteString("\n")
		}

		sb.WriteString("\n")

		// Keyboard shortcut hints.
		hints := []string{"[s] Settings"}
		if len(m.uninstalledProfiles()) > 0 {
			hints = append(hints, "[i] Install agents")
		}
		hints = append(hints, "[q] Quit")
		sb.WriteString(dimStyle.Render("  " + strings.Join(hints, "  ")))
		sb.WriteString("\n")

		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Selection: "))

		if m.buildVersion != "" {
			sb.WriteString("\n\n")
			sb.WriteString(dimStyle.Render("Aperture " + m.buildVersion))
			sb.WriteString("\n")
		}

	case stepSelectProvider:
		sb.WriteString(titleStyle.Render(fmt.Sprintf("Choose a provider for %s:", m.chosenProfile.Name())))
		sb.WriteString("\n")

		for i, prov := range m.providerItems {
			label := fmt.Sprintf("  [%d] %s", i+1, prov.DisplayName())
			if prov.Description != "" {
				label += "  " + dimStyle.Render(prov.Description)
			}
			if m.providerCursor == i {
				sb.WriteString(selectedStyle.Render(label))
			} else {
				sb.WriteString(label)
			}
			sb.WriteString("\n")
		}

		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render("Selection: "))

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
		sb.WriteString(titleStyle.Render(fmt.Sprintf("Choose a backend for %s via %s:",
			m.chosenProfile.Name(), m.chosenProvider.DisplayName())))
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

	case stepSelectModel:
		sb.WriteString(titleStyle.Render(fmt.Sprintf("Choose a default model for %s via %s:",
			m.chosenProfile.Name(), m.chosenProvider.DisplayName())))
		sb.WriteString("\n")

		for i, model := range m.modelItems {
			label := fmt.Sprintf("  [%d] %s", i+1, model)
			if m.modelCursor == i {
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
			if _, isHA := m.chosenProfile.(profiles.HostAwareInstaller); isHA {
				sb.WriteString("  " + inst.InstallHint() + "\n")
			} else {
				sb.WriteString("  This will run: " + inst.InstallHint() + "\n")
			}
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
	}

	return sb.String()
}
