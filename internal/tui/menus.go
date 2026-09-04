package tui

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

const (
	rootTitle       = "Which editor do you want to use?"
	endpointsTitle  = "Aperture Endpoints"
	setupGuideTitle = "Getting Started"
)

// rootMenu is the top-level client picker. It shows installed clients in
// registration order and prepends a [0] quick-select row when any client's
// Replay() is ready to re-launch the last session.
func (m *model) rootMenu() *menu.Menu {
	all := registeredClients(m.g)
	var installed []clients.Client
	var uninstalled []clients.Client
	for _, c := range all {
		if c.IsInstalled() {
			installed = append(installed, c)
		} else {
			uninstalled = append(uninstalled, c)
		}
	}

	items := make([]menu.MenuItem, 0, len(installed)+3)

	// [0] quick-select, if a client can replay the last launch.
	if cmd, quick := m.quickSelect(); cmd != nil {
		items = append(items, menu.MenuItem{
			Digit:  menu.DigitZero,
			Label:  "Quick select: " + quick,
			Action: func() menu.Result { return menu.Result{Cmd: cmd, PopOnDone: true} },
		})
	}

	for _, c := range installed {
		it := c.Menu(m.g)
		if it.Action == nil {
			continue
		}
		items = append(items, it)
	}

	hints := []string{"[s] Settings"}
	if len(uninstalled) > 0 {
		hints = append(hints, "[i] Install agents")
	}
	hints = append(hints, "[q] Quit")

	// Shortcut-only items (hidden so they don't take a number but are
	// activated via their Shortcut key).
	items = append(items, menu.MenuItem{
		Label:    "Settings",
		Shortcut: "s",
		Hidden:   true,
		Action:   func() menu.Result { return menu.Result{Next: m.settingsMenu()} },
	})
	if len(uninstalled) > 0 {
		items = append(items, menu.MenuItem{
			Label:    "Install agents",
			Shortcut: "i",
			Hidden:   true,
			Action:   func() menu.Result { return menu.Result{Next: m.installAgentsMenu()} },
		})
	}

	return &menu.Menu{
		Title: rootTitle,
		Items: items,
		Hint:  strings.Join(hints, "  "),
	}
}

// quickSelect returns the tea.Cmd that replays the last successful launch
// and the human-readable label to render next to [0]. Returns nil if the
// recorded endpoint is not active or the client selection is stale.
func (m *model) quickSelect() (tea.Cmd, string) {
	if m.g.LastLaunch.LastClientName == "" {
		return nil, ""
	}
	for _, c := range registeredClients(m.g) {
		if c.Name() != m.g.LastLaunch.LastClientName {
			continue
		}
		saved, hasSavedEndpoint := m.lastLaunchEndpoint()
		// Legacy launch state did not identify its endpoint. Do not silently
		// replay it against whichever endpoint happens to be active now.
		if !hasSavedEndpoint || !m.endpointConfigured(saved) {
			return nil, ""
		}
		if !sameEndpoint(saved, m.g.ActiveEndpoint()) {
			return nil, ""
		}
		if cmd := c.Replay(m.g); cmd != nil {
			label := c.QuickSelectLabel(m.g)
			label += " @ " + m.endpointLabel(saved)
			return cmd, label
		}
		return nil, ""
	}
	return nil, ""
}

func (m *model) lastLaunchEndpoint() (config.Endpoint, bool) {
	if m.g.LastLaunch.LastEndpointURL == "" {
		return config.Endpoint{}, false
	}
	return config.Endpoint{
		URL:      m.g.LastLaunch.LastEndpointURL,
		BridgeID: m.g.LastLaunch.LastBridgeID,
	}, true
}

func (m *model) endpointConfigured(want config.Endpoint) bool {
	for _, ep := range m.g.Settings.Endpoints {
		if sameEndpoint(ep, want) {
			return true
		}
	}
	return false
}

func sameEndpoint(a, b config.Endpoint) bool {
	return a.URL == b.URL && a.BridgeID == b.BridgeID
}

func endpointFromInput(value, bridgeID string) (config.Endpoint, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return config.Endpoint{}, fmt.Errorf("endpoint URL must be an absolute http or https URL")
	}
	return config.Endpoint{URL: strings.TrimRight(value, "/"), BridgeID: bridgeID}, nil
}

func simpleErrorCmd(err error) tea.Cmd {
	return func() tea.Msg { return menu.SimpleDoneMsg{Err: err} }
}

// settingsMenu is the top-level Settings page: endpoints, uninstall, YOLO toggle.
func (m *model) settingsMenu() *menu.Menu {
	yolo := "off"
	if m.g.Settings.YoloMode {
		yolo = "on"
	}
	return &menu.Menu{
		Title: "Settings",
		Items: []menu.MenuItem{
			{
				Label:  "Bridges",
				Action: func() menu.Result { return menu.Result{Next: m.bridgesMenu()} },
			},
			{
				Label:  "Aperture Endpoints",
				Action: func() menu.Result { return menu.Result{Next: m.endpointsMenu()} },
			},
			{
				Label:  "Uninstall",
				Action: func() menu.Result { return menu.Result{Next: m.uninstallMenu()} },
			},
			{
				Label: "YOLO mode: " + yolo,
				Action: func() menu.Result {
					_ = m.g.SetYolo(!m.g.Settings.YoloMode)
					return menu.Result{Replace: m.settingsMenu()}
				},
			},
		},
		Hint: "Enter to select · Esc to go back",
	}
}

func (m *model) bridgesMenu() *menu.Menu {
	items := []menu.MenuItem{
		{
			Label:    "Bridges connect Aperture through an embedded Tailscale node, so this host does not need tailscaled running.",
			Disabled: true,
		},
	}
	for _, p := range m.g.Settings.Bridges {
		p := p
		items = append(items, menu.MenuItem{
			Label:       p.Name,
			Description: p.ID,
			Action:      func() menu.Result { return menu.Result{} },
		})
	}
	items = append(items, menu.MenuItem{
		Label:    "add",
		Shortcut: "a",
		Hidden:   true,
		Action: func() menu.Result {
			m.promptForInput("Add Bridge:", "Name", func(v string) tea.Cmd {
				if _, err := m.g.AddBridge(v); err != nil {
					return func() tea.Msg { return menu.SimpleDoneMsg{Err: err} }
				}
				m.refreshBridgesMenu()
				return nil
			})
			return menu.Result{}
		},
	})
	items = append(items, menu.MenuItem{
		Label:    "delete",
		Shortcut: "d",
		Hidden:   true,
		Action: func() menu.Result {
			idx := m.cursor() - 1
			if idx < 0 || idx >= len(m.g.Settings.Bridges) {
				return menu.Result{}
			}
			if err := m.g.RemoveBridge(m.g.Settings.Bridges[idx].ID); err != nil {
				return errResult(err.Error())
			}
			return menu.Result{Replace: m.bridgesMenu()}
		},
	})
	return &menu.Menu{
		Title: "Bridges",
		Items: items,
		Hint:  "d to remove · a to add · Esc to go back",
	}
}

// endpointsMenu lists configured endpoints with add/delete affordances.
// Selecting an entry runs preflight and promotes it only after success.
func (m *model) endpointsMenu() *menu.Menu {
	items := make([]menu.MenuItem, 0, len(m.g.Settings.Endpoints)+3)
	for i, ep := range m.g.Settings.Endpoints {
		ep := ep
		label := m.endpointLabel(ep)
		if i == 0 {
			label = greenStyle.Render(label + " (active)")
		}
		items = append(items, menu.MenuItem{
			Label: label,
			Action: func() menu.Result {
				return menu.Result{Cmd: m.activateEndpointCmd(ep)}
			},
		})
	}
	// Hidden: "a" opens the endpoint connection flow. Surfaced via the footer hint.
	items = append(items, menu.MenuItem{
		Label:    "add",
		Shortcut: "a",
		Hidden:   true,
		Action:   func() menu.Result { return menu.Result{Next: m.addEndpointConnectionMenu()} },
	})
	// Hidden: "d" deletes the row under the cursor.
	items = append(items, menu.MenuItem{
		Label:    "delete",
		Shortcut: "d",
		Hidden:   true,
		Action: func() menu.Result {
			idx := m.cursor()
			if idx < 0 || idx >= len(m.g.Settings.Endpoints) || len(m.g.Settings.Endpoints) <= 1 {
				return menu.Result{}
			}
			if idx == 0 {
				return errResult("switch to another endpoint before removing the active endpoint")
			}
			removed := m.g.Settings.Endpoints[idx]
			if err := m.g.RemoveEndpoint(idx); err != nil {
				return errResult(err.Error())
			}
			if m.failedEndpoint != nil && sameEndpoint(*m.failedEndpoint, removed) {
				m.clearEndpointFailure()
				m.resetStack(m.rootMenu())
				return menu.Result{}
			}
			return menu.Result{Replace: m.endpointsMenu()}
		},
	})

	return &menu.Menu{
		Title: endpointsTitle,
		Items: items,
		Hint:  "Enter to select · d to remove · a to add · Esc to go back",
		OnBack: func() tea.Cmd {
			if len(m.stack) <= 1 {
				if m.forcedToEndpoint {
					return m.quitCmd()
				}
				return nil
			}
			m.popOne()
			return tea.ClearScreen
		},
	}
}

// setupGuideMenu is shown when endpoint activation or its /v1/models check
// fails. A candidate endpoint remains configured, but the previous working
// endpoint stays active until the candidate passes both stages.
func (m *model) setupGuideMenu() *menu.Menu {
	target := m.g.ActiveEndpoint()
	if m.failedEndpoint != nil {
		target = *m.failedEndpoint
	}

	var preamble string
	if target.BridgeID != "" {
		bridgeName := target.BridgeID
		if bridge, ok := m.g.Bridge(target.BridgeID); ok {
			bridgeName = bridge.Name
		}
		preamble = "Could not reach Aperture at " + target.URL + " through bridge " + bridgeName + ".\n\n" +
			"The bridge uses an embedded Tailscale node; this machine does not need Tailscale installed or running."
	} else {
		switch checkTailscale() {
		case tsNotInstalled:
			preamble = "Could not reach Aperture at " + target.URL + ".\n\nTailscale is not installed.\nInstall it from: https://tailscale.com/download"
		case tsNotRunning:
			preamble = "Could not reach Aperture at " + target.URL + ".\n\nTailscale is installed but not running.\nStart Tailscale, then retry."
		case tsNotConnected:
			preamble = "Could not reach Aperture at " + target.URL + ".\n\nTailscale is not connected to a network.\nLog in with: tailscale up"
		case tsConnected:
			preamble = "Tailscale is connected.\n\nCould not reach Aperture at " + target.URL + ".\nEither:\n  - set up an Aperture instance at https://aperture.tailscale.com/\n  - or enter a different Aperture URL below"
		}
	}

	hasPrevious := m.connected && !sameEndpoint(target, m.g.ActiveEndpoint())
	if hasPrevious {
		preamble += "\n\nThe previous endpoint remains active: " + m.endpointLabel(m.g.ActiveEndpoint()) + "."
	}

	items := []menu.MenuItem{
		{
			Label: "Retry connection",
			Action: func() menu.Result {
				return menu.Result{Cmd: m.activateEndpointCmd(target)}
			},
		},
		{
			Label: "Edit endpoint URL",
			Action: func() menu.Result {
				m.promptForInput("Edit Endpoint:", "Current: "+target.URL, func(v string) tea.Cmd {
					next, err := endpointFromInput(v, target.BridgeID)
					if err != nil {
						return simpleErrorCmd(err)
					}
					if err := m.g.ReplaceEndpoint(target, next); err != nil {
						return simpleErrorCmd(err)
					}
					m.failedEndpoint = &next
					return m.activateEndpointCmd(next)
				})
				return menu.Result{}
			},
		},
		{
			Label: "Connection options",
			Action: func() menu.Result {
				return menu.Result{Next: m.endpointsMenu()}
			},
		},
	}
	if hasPrevious {
		items = append(items, menu.MenuItem{
			Label: "Return to active endpoint",
			Action: func() menu.Result {
				m.clearEndpointFailure()
				return menu.Result{Replace: m.rootMenu()}
			},
		})
	}
	if m.endpointConfigured(target) && !sameEndpoint(target, m.g.ActiveEndpoint()) {
		items = append(items, menu.MenuItem{
			Label: "Remove endpoint",
			Action: func() menu.Result {
				for i, ep := range m.g.Settings.Endpoints {
					if !sameEndpoint(ep, target) {
						continue
					}
					if err := m.g.RemoveEndpoint(i); err != nil {
						return errResult(err.Error())
					}
					break
				}
				m.clearEndpointFailure()
				return menu.Result{Replace: m.rootMenu()}
			},
		})
	}

	onBack := func() tea.Cmd { return m.quitCmd() }
	if hasPrevious {
		onBack = func() tea.Cmd {
			m.clearEndpointFailure()
			m.resetStack(m.rootMenu())
			return tea.ClearScreen
		}
	}
	return &menu.Menu{
		Title:    setupGuideTitle,
		Preamble: preamble,
		Items:    items,
		Hint:     "Enter to select · Esc to go back",
		OnBack:   onBack,
	}
}

func (m *model) clearEndpointFailure() {
	m.failedEndpoint = nil
	m.preflightErr = ""
	m.forcedToEndpoint = false
}

func (m *model) addEndpointConnectionMenu() *menu.Menu {
	return &menu.Menu{
		Title: "Endpoint Connection",
		Items: []menu.MenuItem{
			{
				Label: "Direct",
				Action: func() menu.Result {
					m.promptForInput("Add Direct Endpoint:", "URL", func(v string) tea.Cmd {
						ep, err := endpointFromInput(v, "")
						if err != nil {
							return simpleErrorCmd(err)
						}
						if err := m.g.UpsertEndpoint(ep); err != nil {
							return simpleErrorCmd(err)
						}
						return m.activateEndpointCmd(ep)
					})
					return menu.Result{}
				},
			},
			{
				Label:  "Bridge",
				Action: func() menu.Result { return menu.Result{Next: m.endpointBridgeMenu()} },
			},
		},
		Hint: "Enter to select · Esc to go back",
	}
}

func (m *model) endpointBridgeMenu() *menu.Menu {
	items := make([]menu.MenuItem, 0, len(m.g.Settings.Bridges)+2)
	if len(m.g.Settings.Bridges) == 0 {
		items = append(items, menu.MenuItem{
			Label:    "No bridges configured.",
			Disabled: true,
		})
	}
	for _, p := range m.g.Settings.Bridges {
		p := p
		items = append(items, menu.MenuItem{
			Label:       p.Name,
			Description: p.ID,
			Action: func() menu.Result {
				m.promptForBridgeEndpoint(p)
				return menu.Result{}
			},
		})
	}
	items = append(items, menu.MenuItem{
		Label: "Add Bridge",
		Action: func() menu.Result {
			m.promptForInput("Add Bridge:", "Name", func(v string) tea.Cmd {
				bridge, err := m.g.AddBridge(v)
				if err != nil {
					return simpleErrorCmd(err)
				}
				m.refreshMenuByTitle("Choose a bridge", m.endpointBridgeMenu())
				m.promptForBridgeEndpoint(bridge)
				return nil
			})
			return menu.Result{}
		},
	})
	return &menu.Menu{
		Title: "Choose a bridge",
		Items: items,
		Hint:  "Enter to select or add · Esc to go back",
	}
}

func (m *model) promptForBridgeEndpoint(bridge config.Bridge) {
	m.promptForInput("Add Bridge Endpoint:", "URL", func(v string) tea.Cmd {
		ep, err := endpointFromInput(v, bridge.ID)
		if err != nil {
			return simpleErrorCmd(err)
		}
		if err := m.g.UpsertEndpoint(ep); err != nil {
			return simpleErrorCmd(err)
		}
		return m.activateEndpointCmd(ep)
	})
}

func (m *model) endpointLabel(ep config.Endpoint) string {
	if ep.BridgeID == "" {
		return ep.URL + " (direct)"
	}
	if p, ok := m.g.Bridge(ep.BridgeID); ok {
		return ep.URL + " via " + p.Name
	}
	return ep.URL + " via " + ep.BridgeID
}

// installAgentsMenu lists uninstalled clients and confirms/runs each install.
func (m *model) installAgentsMenu() *menu.Menu {
	var items []menu.MenuItem
	for _, c := range registeredClients(m.g) {
		if c.IsInstalled() {
			continue
		}
		c := c
		items = append(items, menu.MenuItem{
			Label:  c.Name(),
			Action: func() menu.Result { return menu.Result{Next: m.installConfirmMenu(c)} },
		})
	}
	if len(items) == 0 {
		return &menu.Menu{
			Title: "Install agents",
			Items: []menu.MenuItem{{Label: "All agents installed.", Disabled: true}},
			Hint:  "Esc to go back",
		}
	}
	return &menu.Menu{
		Title: "Install agents",
		Items: items,
		Hint:  "Enter to select · Esc to go back",
	}
}

func (m *model) installConfirmMenu(c clients.Client) *menu.Menu {
	plan := c.Install(m.g)
	return &menu.Menu{
		Title: "Install " + c.Name() + "?",
		Items: []menu.MenuItem{
			{Label: plan.Hint, Disabled: true},
			{
				Label:    "Install",
				Shortcut: "y",
				Action: func() menu.Result {
					if plan.Run == nil {
						return menu.Result{Pop: true}
					}
					return menu.Result{Cmd: runInstallCmd(c, plan), PopOnDone: true}
				},
			},
			{
				Label:    "Cancel",
				Shortcut: "n",
				Action:   func() menu.Result { return menu.Result{Pop: true} },
			},
		},
		Hint: "y to install · n to cancel",
	}
}

// uninstallMenu lists installed clients and confirms/runs uninstall.
func (m *model) uninstallMenu() *menu.Menu {
	var items []menu.MenuItem
	for _, c := range registeredClients(m.g) {
		if !c.IsInstalled() {
			continue
		}
		c := c
		items = append(items, menu.MenuItem{
			Label:  c.Name(),
			Action: func() menu.Result { return menu.Result{Next: m.uninstallConfirmMenu(c)} },
		})
	}
	if len(items) == 0 {
		return &menu.Menu{
			Title: "Uninstall",
			Items: []menu.MenuItem{{Label: "No agents installed.", Disabled: true}},
			Hint:  "Esc to go back",
		}
	}
	return &menu.Menu{
		Title: "Uninstall",
		Items: items,
		Hint:  "Enter to select · Esc to go back",
	}
}

func (m *model) uninstallConfirmMenu(c clients.Client) *menu.Menu {
	plan := c.Uninstall()
	if plan.Run == nil {
		return &menu.Menu{
			Title: c.Name(),
			Items: []menu.MenuItem{
				{Label: plan.Hint, Disabled: true},
				{Label: "OK", Shortcut: "y", Action: func() menu.Result { return menu.Result{Pop: true} }},
			},
			Hint: "Enter to go back",
		}
	}
	return &menu.Menu{
		Title: "Uninstall " + c.Name() + "?",
		Items: []menu.MenuItem{
			{Label: "This will run: " + plan.Hint, Disabled: true},
			{
				Label:    "Uninstall",
				Shortcut: "y",
				Action: func() menu.Result {
					return menu.Result{Cmd: runUninstallFn(plan.Run)}
				},
			},
			{
				Label:    "Cancel",
				Shortcut: "n",
				Action:   func() menu.Result { return menu.Result{Pop: true} },
			},
		},
		Hint: "y to uninstall · n to cancel",
	}
}

// runInstallCmd returns a tea.Cmd that runs the provided install command with
// terminal takeover (so the user sees download progress). A zero exit status
// is successful only if the client binary can then be found.
func runInstallCmd(client clients.Client, plan clients.InstallPlan) tea.Cmd {
	cmd, err := plan.Run()
	if err != nil {
		return func() tea.Msg { return menu.InstallDoneMsg{Err: err} }
	}
	if cmd == nil {
		return func() tea.Msg { return installDoneMsg(client, plan.SkipInstalledCheck, nil) }
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return installDoneMsg(client, plan.SkipInstalledCheck, err)
	})
}

func installDoneMsg(client clients.Client, skipInstalledCheck bool, err error) menu.InstallDoneMsg {
	if err == nil && !skipInstalledCheck && !client.IsInstalled() {
		err = fmt.Errorf("%s installer completed, but %q was not found on PATH or in a known install location", client.Name(), client.BinaryName())
	}
	return menu.InstallDoneMsg{Err: err}
}

// runUninstallFn returns a tea.Cmd that invokes the uninstall function and
// emits menu.InstallDoneMsg (we reuse the install-done flow to re-scan the
// client list on completion).
func runUninstallFn(run func() error) tea.Cmd {
	return func() tea.Msg {
		return menu.InstallDoneMsg{Err: run()}
	}
}

// errResult is a small helper to emit an error through the shared done-msg
// channel from a menu builder.
func errResult(msg string) menu.Result {
	return menu.Result{Cmd: func() tea.Msg {
		return menu.SimpleDoneMsg{Err: fmt.Errorf("%s", msg)}
	}}
}
