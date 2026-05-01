package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/menu"
)

const (
	rootTitle      = "Which editor do you want to use?"
	endpointsTitle = "Aperture Endpoints"
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
// and the human-readable label to render next to [0]. Returns nil if no
// client claims the last launch or its state is stale.
func (m *model) quickSelect() (tea.Cmd, string) {
	if m.g.LastLaunch.LastClientName == "" {
		return nil, ""
	}
	for _, c := range registeredClients(m.g) {
		cmd := c.Replay(m.g)
		if cmd != nil {
			return cmd, c.QuickSelectLabel(m.g)
		}
	}
	return nil, ""
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

// endpointsMenu lists configured endpoints with add/delete affordances.
// Selecting an entry rotates it to the front and re-runs preflight.
func (m *model) endpointsMenu() *menu.Menu {
	items := make([]menu.MenuItem, 0, len(m.g.Settings.Endpoints)+3)
	for i, ep := range m.g.Settings.Endpoints {
		url := ep.URL
		label := url
		if i == 0 {
			label = greenStyle.Render(url + " (active)")
		}
		items = append(items, menu.MenuItem{
			Label: label,
			Action: func() menu.Result {
				if err := m.g.SetApertureHost(url); err != nil {
					return errResult(err.Error())
				}
				m.step = stepPreflight
				return menu.Result{Cmd: runPreflight(url)}
			},
		})
	}
	items = append(items, menu.MenuItem{
		Label:    "Add endpoint",
		Shortcut: "a",
		Action: func() menu.Result {
			m.promptForInput("Add Endpoint:", "", func(v string) tea.Cmd {
				_ = m.g.UpsertEndpoint(v)
				if len(m.stack) > 0 {
					m.stack[len(m.stack)-1] = m.endpointsMenu()
					m.cursors[len(m.cursors)-1] = 0
				}
				return nil
			})
			return menu.Result{}
		},
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
			_ = m.g.RemoveEndpoint(idx)
			return menu.Result{Replace: m.endpointsMenu()}
		},
	})

	backHint := "Esc to go back"
	if m.forcedToEndpoint {
		backHint = "Esc to quit"
	}

	return &menu.Menu{
		Title: endpointsTitle,
		Items: items,
		Hint:  "Enter to select · d to remove · a to add · " + backHint,
		OnBack: func() tea.Cmd {
			if m.forcedToEndpoint {
				return tea.Quit
			}
			m.popOne()
			return tea.ClearScreen
		},
	}
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
					return menu.Result{Cmd: runInstallCmd(plan.Run), PopOnDone: true}
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

// runInstallCmd returns a tea.Cmd that runs the provided install command
// with terminal takeover (so the user sees download progress) and emits
// menu.InstallDoneMsg on completion.
func runInstallCmd(producer func() (*exec.Cmd, error)) tea.Cmd {
	cmd, err := producer()
	if err != nil {
		return func() tea.Msg { return menu.InstallDoneMsg{Err: err} }
	}
	if cmd == nil {
		return func() tea.Msg { return menu.InstallDoneMsg{} }
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return menu.InstallDoneMsg{Err: err}
	})
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
