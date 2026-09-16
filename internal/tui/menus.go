package tui

import (
	"fmt"
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

	hints := []string{"[c] Change connection", "[s] Settings"}
	if len(uninstalled) > 0 {
		hints = append(hints, "[i] Install agents")
	}
	hints = append(hints, "[q] Quit")

	// Shortcut-only items (hidden so they don't take a number but are
	// activated via their Shortcut key).
	// Listed before Settings in the hints: with a reachable default the
	// launcher connects on its own, and a second bridge or tailnet is
	// otherwise unreachable from here.
	items = append(items, menu.MenuItem{
		Label:    "Change connection",
		Shortcut: "c",
		Hidden:   true,
		Action:   func() menu.Result { return menu.Result{Next: m.endpointsMenu()} },
	})
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
			Description: m.bridgeRowDescription(p),
			Action:      func() menu.Result { return menu.Result{Cmd: m.connectBridgeCmd(p)} },
		})
	}
	items = append(items, menu.MenuItem{
		Label:    "add",
		Shortcut: "a",
		Hidden:   true,
		Action: func() menu.Result {
			m.promptForInput("Add Bridge:", "Name", "", func(v string) tea.Cmd {
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
		Hint:  "Enter to connect · d to remove · a to add · Esc to go back",
	}
}

// bridgeRowDescription labels a bridge with the tailnet it reaches, falling
// back to its ID when no connection has reported one yet.
func (m *model) bridgeRowDescription(bridge config.Bridge) string {
	if name := m.bridgeTailnet(bridge); name != "" {
		return "tailnet " + name
	}
	return bridge.ID
}

// endpointsMenu is the connection picker: every Aperture this launcher can
// reach, one row each, whether it is a saved endpoint or a bridge that has no
// endpoint yet. Selecting a row opens its page rather than connecting straight
// away, so connecting, retargeting, switching tailnet and removing are all on
// screen instead of behind remembered keys.
func (m *model) endpointsMenu() *menu.Menu {
	rows := m.connectionRows()
	items := make([]menu.MenuItem, 0, len(rows)+4)
	for _, row := range rows {
		items = append(items, menu.MenuItem{
			Label:       m.connectionLabel(row),
			Description: m.connectionDescription(row),
			Action:      func() menu.Result { return menu.Result{Next: m.connectionMenu(row)} },
		})
	}
	items = append(items, menu.MenuItem{
		Label:  "Add a connection",
		Action: func() menu.Result { return menu.Result{Next: m.addEndpointConnectionMenu()} },
	})
	// "a" still adds, as it did before the rows above existed. Surfaced via the
	// footer hint, like the "e" and "d" aliases below it.
	items = append(items, menu.MenuItem{
		Label:    "add",
		Shortcut: "a",
		Hidden:   true,
		Action:   func() menu.Result { return menu.Result{Next: m.addEndpointConnectionMenu()} },
	})
	// Hidden: "e" retargets the row under the cursor. Surfaced via the footer hint.
	items = append(items, menu.MenuItem{
		Label:    "edit",
		Shortcut: "e",
		Hidden:   true,
		Action: func() menu.Result {
			row, ok := m.connectionAtCursor()
			if !ok {
				return menu.Result{}
			}
			if !row.saved {
				return errResult("connect through " + row.bridge.Name + " first, then its URL can be changed")
			}
			m.promptEditEndpoint(row.ep)
			return menu.Result{}
		},
	})
	// Hidden: "d" deletes the row under the cursor.
	items = append(items, menu.MenuItem{
		Label:    "delete",
		Shortcut: "d",
		Hidden:   true,
		Action: func() menu.Result {
			row, ok := m.connectionAtCursor()
			if !ok {
				return menu.Result{}
			}
			return m.removeConnectionRow(row)
		},
	})

	return &menu.Menu{
		Title: endpointsTitle,
		Items: items,
		Hint:  "Enter for connection options · a to add · e to edit · d to remove · Esc to go back",
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

// connectionRow is one line on the connection picker. A saved endpoint is one
// row; so is a bridge nothing points at yet, described by the endpoint it would
// create, because a bridge with no endpoint is still a connection the user has
// to be able to pick. That is how a second tailnet gets reached the first time.
type connectionRow struct {
	ep     config.Endpoint
	bridge config.Bridge
	saved  bool // ep is in Settings.Endpoints
	active bool
}

func (m *model) connectionRows() []connectionRow {
	rows := make([]connectionRow, 0, len(m.g.Settings.Endpoints)+len(m.g.Settings.Bridges))
	used := make(map[string]bool, len(m.g.Settings.Bridges))
	for i, ep := range m.g.Settings.Endpoints {
		row := connectionRow{ep: ep, saved: true, active: i == 0}
		if ep.BridgeID != "" {
			used[ep.BridgeID] = true
			row.bridge, _ = m.g.Bridge(ep.BridgeID)
		}
		rows = append(rows, row)
	}
	// Endpoint rows first: the hidden "e" and "d" aliases index
	// Settings.Endpoints by cursor position.
	for _, b := range m.g.Settings.Bridges {
		if used[b.ID] {
			continue
		}
		rows = append(rows, connectionRow{
			ep:     config.Endpoint{URL: config.DefaultLocation, BridgeID: b.ID},
			bridge: b,
		})
	}
	return rows
}

// connectionAtCursor resolves the picker row the cursor is on. The hidden "e"
// and "d" aliases act through it rather than indexing Settings.Endpoints, so
// they see the same rows the user does: a bridge with no endpoint is a row too,
// and indexing past the endpoints made those keys silently do nothing.
func (m *model) connectionAtCursor() (connectionRow, bool) {
	rows := m.connectionRows()
	idx := m.cursor()
	if idx < 0 || idx >= len(rows) {
		return connectionRow{}, false
	}
	return rows[idx], true
}

func (m *model) connectionLabel(row connectionRow) string {
	if !row.saved {
		return "Connect via " + row.bridge.Name
	}
	label := m.endpointLabel(row.ep)
	if row.active {
		return greenStyle.Render(label + " (active)")
	}
	return label
}

// connectionDescription names the tailnet behind a bridge row. Which bridge to
// use is a choice between tailnets, so the row has to say which one it reaches
// before it is picked.
func (m *model) connectionDescription(row connectionRow) string {
	if row.ep.BridgeID == "" {
		return ""
	}
	if name := m.bridgeTailnet(row.bridge); name != "" {
		return "tailnet " + name
	}
	return "tailnet not known yet"
}

// bridgeTailnet prefers what the running node reports to what was saved: a
// bridge that switched tailnets this session leaves a stale name on disk until
// the next successful connection rewrites it.
func (m *model) bridgeTailnet(bridge config.Bridge) string {
	if name := m.bridgeManager.Tailnet(bridge.ID); name != "" {
		return name
	}
	return bridge.Tailnet
}

// connectionMenu is one connection's page. Every action it offers is a row:
// the picker is the only way to reach a second bridge, so its actions cannot
// be keys the user has to already know about.
func (m *model) connectionMenu(row connectionRow) *menu.Menu {
	title := row.bridge.Name
	if row.saved {
		title = m.endpointLabel(row.ep)
	}

	connect, target := "Connect", row.ep.URL
	if row.active && m.connected {
		connect = "Reconnect"
	}
	if !row.saved {
		// Nothing has named a URL for this bridge yet, so the connection is
		// about to guess one. Say so rather than showing a bare URL the user
		// never typed.
		target = "looks for Aperture at " + row.ep.URL
	}
	items := []menu.MenuItem{{
		Label:       connect,
		Description: target,
		Action: func() menu.Result {
			return menu.Result{Cmd: m.connectVia(row.ep, false)}
		},
	}}

	if row.saved {
		items = append(items, menu.MenuItem{
			Label:       "Change URL",
			Description: "now " + row.ep.URL,
			Action: func() menu.Result {
				m.promptEditEndpoint(row.ep)
				return menu.Result{}
			},
		})
	}

	if row.ep.BridgeID != "" {
		description := "log the bridge out and sign in to a different tailnet"
		if name := m.bridgeTailnet(row.bridge); name != "" {
			description = "leave " + name + " and sign in to a different tailnet"
		}
		items = append(items, menu.MenuItem{
			Label:       "Switch tailnet",
			Description: description,
			Action:      func() menu.Result { return menu.Result{Next: m.switchTailnetMenu(row)} },
		})
	}

	switch {
	case row.active:
		items = append(items, menu.MenuItem{
			Label:       "Remove connection",
			Description: "connect to another one first",
			Disabled:    true,
		})
	case row.saved:
		items = append(items, menu.MenuItem{
			Label:  "Remove connection",
			Action: func() menu.Result { return m.removeConnectionRow(row) },
		})
	default:
		items = append(items, menu.MenuItem{
			Label:       "Remove bridge",
			Description: row.bridge.ID,
			Action:      func() menu.Result { return m.removeConnectionRow(row) },
		})
	}

	return &menu.Menu{
		Title: title,
		Items: items,
		Hint:  "Enter to select · Esc to go back",
	}
}

// removeConnectionRow deletes what a picker row stands for: the endpoint, or
// the bridge itself when no endpoint points at it yet. Shared by the row's page
// and the "d" key, which have to agree on what removing a row means.
func (m *model) removeConnectionRow(row connectionRow) menu.Result {
	switch {
	case row.active:
		return errResult("connect to another endpoint before removing the active one")
	case row.saved:
		return m.removeConnection(row.ep)
	default:
		if err := m.g.RemoveBridge(row.bridge.ID); err != nil {
			return errResult(err.Error())
		}
		m.refreshEndpointsMenu()
		return menu.Result{Cmd: tea.ClearScreen}
	}
}

func (m *model) removeConnection(ep config.Endpoint) menu.Result {
	for i, existing := range m.g.Settings.Endpoints {
		if !sameEndpoint(existing, ep) {
			continue
		}
		if i == 0 {
			return errResult("connect to another endpoint before removing the active one")
		}
		if err := m.g.RemoveEndpoint(i); err != nil {
			return errResult(err.Error())
		}
		break
	}
	if m.failedEndpoint != nil && sameEndpoint(*m.failedEndpoint, ep) {
		m.clearEndpointFailure()
		m.resetStack(m.rootMenu())
		return menu.Result{Cmd: tea.ClearScreen}
	}
	m.refreshEndpointsMenu()
	return menu.Result{Cmd: tea.ClearScreen}
}

// switchTailnetMenu confirms logging a bridge out. A bridge holds one tailnet
// at a time, so switching is destructive in a way connecting is not: the node
// leaves the tailnet it is on, and getting back needs another login.
func (m *model) switchTailnetMenu(row connectionRow) *menu.Menu {
	preamble := "A bridge is on one tailnet at a time."
	if name := m.bridgeTailnet(row.bridge); name != "" {
		preamble += " " + row.bridge.Name + " is on " + name + " now."
	}
	preamble += "\n\nSwitching logs the bridge out, removing its node from that tailnet, then prints a login link. Open the link and pick the tailnet you want; " +
		row.ep.URL + " is looked for there."
	return &menu.Menu{
		Title:    "Switch tailnet for " + row.bridge.Name + "?",
		Preamble: preamble,
		Items: []menu.MenuItem{
			{
				Label:    "Switch tailnet",
				Shortcut: "y",
				Action: func() menu.Result {
					// Drop the recorded name now: an abandoned login would
					// otherwise leave the picker naming a tailnet the bridge
					// has already left.
					if err := m.g.SetBridgeTailnet(row.bridge.ID, ""); err != nil {
						return errResult(err.Error())
					}
					return menu.Result{Cmd: m.connectVia(row.ep, true)}
				},
			},
			{
				Label:    "Cancel",
				Shortcut: "n",
				Action:   func() menu.Result { return menu.Result{Pop: true} },
			},
		},
		Hint: "y to switch · n to cancel",
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
		if target.URL == config.DefaultLocation {
			preamble += "\n\n" + config.DefaultLocation + " is the default Aperture location. " +
				"If yours answers on a different hostname, edit the endpoint URL below."
		}
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
				m.promptEditEndpoint(target)
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

// promptEditEndpoint edits ep's URL in place and connects to what the user
// typed, keeping whichever bridge ep is reached through. It is the only way to
// retarget an endpoint that connects successfully: the guessed default answers
// on any tailnet with a host called "ai", and a success shows neither the
// connect screen's inline override nor the setup guide's editor.
func (m *model) promptEditEndpoint(ep config.Endpoint) {
	m.promptForInput("Edit Endpoint:", "URL", ep.URL, func(v string) tea.Cmd {
		next, err := config.ParseEndpoint(v, ep.BridgeID)
		if err != nil {
			return simpleErrorCmd(err)
		}
		if err := m.g.ReplaceEndpoint(ep, next); err != nil {
			return simpleErrorCmd(err)
		}
		// Follow the rename, so a failure screen already showing ep keeps
		// naming the endpoint the user is now trying.
		if m.failedEndpoint != nil && sameEndpoint(*m.failedEndpoint, ep) {
			m.failedEndpoint = &next
		}
		return m.activateEndpointCmd(next)
	})
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
					m.promptForInput("Add Direct Endpoint:", "URL", "", func(v string) tea.Cmd {
						ep, err := config.ParseEndpoint(v, "")
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
			Description: m.bridgeRowDescription(p),
			Action:      func() menu.Result { return menu.Result{Cmd: m.connectBridgeCmd(p)} },
		})
	}
	items = append(items, menu.MenuItem{
		Label: "Add Bridge",
		Action: func() menu.Result {
			m.promptForInput("Add Bridge:", "Name", "", func(v string) tea.Cmd {
				bridge, err := m.g.AddBridge(v)
				if err != nil {
					return simpleErrorCmd(err)
				}
				m.refreshMenuByTitle("Choose a bridge", m.endpointBridgeMenu())
				return m.connectBridgeCmd(bridge)
			})
			return menu.Result{}
		},
	})
	return &menu.Menu{
		Title: "Choose a bridge",
		Items: items,
		Hint:  "Enter to connect or add · Esc to go back",
	}
}

// connectBridgeCmd starts discovery through bridge: probe the well-known
// Aperture location, the same guess a direct connection starts from, instead
// of demanding a URL the user may not know. The connect screen takes a
// different URL while the guess runs, so knowing it costs no waiting.
func (m *model) connectBridgeCmd(bridge config.Bridge) tea.Cmd {
	return m.connectVia(config.Endpoint{URL: config.DefaultLocation, BridgeID: bridge.ID}, false)
}

// connectVia connects to ep, saving it first when it is not in settings yet so
// the failure screen has something to name, retry and edit. switchTailnet logs
// the bridge out on the way, which is what makes the next connection ask for a
// login instead of reusing the tailnet it is already on.
func (m *model) connectVia(ep config.Endpoint, switchTailnet bool) tea.Cmd {
	ephemeral := !m.endpointConfigured(ep)
	if ephemeral {
		if err := m.g.UpsertEndpoint(ep); err != nil {
			return simpleErrorCmd(err)
		}
	}
	return m.activateEndpoint(ep, ephemeral, switchTailnet)
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
