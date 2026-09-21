package tui

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
	"github.com/tailscale/aperture-cli/internal/menu"
)

// bridgeRemovedMsg carries the outcome of the tailnet round trip back to the
// update loop, where the records can be dropped. endpoint is nil for a bare
// bridge.
type bridgeRemovedMsg struct {
	id       int
	bridge   config.Bridge
	endpoint config.Endpoint
	err      error
}

// destroyBridge is the tailnet round trip a removal makes. A seam for the
// tests: pickerModel has no Machines, and a real Destroy would want a tailnet.
var destroyBridge = func(ctx context.Context, machines *bridges.Machines, bridge config.Bridge, emit func(connection.Event)) error {
	return machines.Destroy(ctx, bridge, emit)
}

// removeRow deletes what a picker row stands for. Shared by the row's page and
// the "d" key, which have to agree on what removing a row means.
func (m *model) removeRow(row connectionRow) menu.Result {
	var ep config.Endpoint
	if row.saved {
		ep = row.ep
	}
	return m.remove(row.bridge, ep)
}

// bridgeOf is the Bridge a saved endpoint is reached through, zero for a
// direct one. The setup guide holds an endpoint rather than a picker row.
func (m *model) bridgeOf(ep config.Endpoint) config.Bridge {
	if bridged, ok := ep.(config.BridgeEndpoint); ok {
		bridge, _ := m.g.Bridge(bridged.BridgeID())
		return bridge
	}
	return config.Bridge{}
}

// remove confirms before a removal that takes a device off a tailnet, and
// otherwise drops the records at once. Every delete in the TUI comes through
// here: the machine outlives settings, so a site that skips this leaves a
// device on the user's tailnet that nothing names any more.
func (m *model) remove(bridge config.Bridge, ep config.Endpoint) menu.Result {
	destroys, err := bridges.DestroysMachine(m.g, bridge, ep)
	if err != nil {
		return errResult(err.Error())
	}
	if !destroys {
		if err := bridges.ForgetBridge(m.g, bridge, ep, nil); err != nil {
			return errResult(err.Error())
		}
		return menu.Result{Cmd: m.afterRemoval(ep)}
	}
	return menu.Result{Next: m.removeBridgeMenu(bridge, ep)}
}

// removeBridgeMenu is the confirmation. Removal is irreversible from here and
// takes a device off the user's tailnet, so the screen names the device by the
// name the admin console shows it under.
func (m *model) removeBridgeMenu(bridge config.Bridge, ep config.Endpoint) *menu.Menu {
	preamble := "Bridge " + bridge.Name + " is the device " + bridges.MachineName(bridge.ID)
	if name := m.machines.Tailnet(bridge); name != "" {
		preamble += " on " + name
	}
	preamble += ".\n\nRemoving it logs that device out of the tailnet and discards the login stored on this machine. " +
		"Connecting through a bridge of this name again is a new device and a new login."
	return &menu.Menu{
		Title:    "Remove bridge " + bridge.Name + "?",
		Preamble: preamble,
		Items: []menu.MenuItem{
			{
				Label:    "Remove",
				Shortcut: "y",
				Action:   func() menu.Result { return menu.Result{Cmd: m.destroyBridgeCmd(bridge, ep)} },
			},
			{
				Label:    "Cancel",
				Shortcut: "n",
				Action:   func() menu.Result { return menu.Result{Pop: true} },
			},
		},
		Hint: "y to remove · n to cancel",
	}
}

// destroyBridgeCmd puts the logout on the connect screen, which is where this
// program already shows slow bridge work and its log tail. The attempt carries
// no cancel handle: settings still name the device, and abandoning the wait
// half way through a logout is how the record and the device disagree.
func (m *model) destroyBridgeCmd(bridge config.Bridge, ep config.Endpoint) tea.Cmd {
	m.stopActivation()
	m.step = stepPreflight
	m.preflightErr = ""
	m.bridgeLogs = nil

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan bridgeLine, 32)
	m.activationSeq++
	act := &activation{
		id:      m.activationSeq,
		label:   "Removing bridge " + bridge.Name + " ...",
		started: time.Now(),
		logCh:   ch,
		logCtx:  ctx,
	}
	m.act = act
	emit := bridgeLogSink(ctx, ch, act.started)
	machines := m.machines
	destroy := func() tea.Msg {
		defer cancel()
		err := destroyBridge(ctx, machines, bridge, emit)
		return bridgeRemovedMsg{id: act.id, bridge: bridge, endpoint: ep, err: err}
	}
	return tea.Batch(destroy, waitBridgeLog(ctx, ch), activationTick(act.id))
}

// bridgeRemoved shows the outcome. Whether the records go is the service's
// call; this only decides which screen says what happened.
func (m *model) bridgeRemoved(msg bridgeRemovedMsg) (tea.Model, tea.Cmd) {
	if m.act == nil || m.act.id != msg.id {
		return m, nil
	}
	m.act = nil
	m.step = stepMenu
	err := bridges.ForgetBridge(m.g, msg.bridge, msg.endpoint, msg.err)
	var unconfirmed *bridges.Unconfirmed
	var cmd tea.Cmd
	switch {
	case errors.As(err, &unconfirmed):
		cmd = m.afterRemoval(msg.endpoint)
		m.step = stepError
		m.errMsg = m.unconfirmedMessage(unconfirmed)
	case err != nil && errors.Is(err, msg.err):
		m.step = stepError
		m.errMsg = "Could not remove bridge " + msg.bridge.Name + ": " + err.Error() +
			"\n\nThe connection is unchanged. Removing it again retries the logout."
	case err != nil:
		m.step = stepError
		m.errMsg = err.Error()
	default:
		cmd = m.afterRemoval(msg.endpoint)
	}
	if m.quitAfterRemoval {
		m.quitAfterRemoval = false
		return m, m.quitCmd()
	}
	return m, cmd
}

// unconfirmedMessage is what the user needs to finish the job by hand: the
// device name, and where to look for it. A bare "timed out" leaves them
// hunting for a machine whose name this program chose.
func (m *model) unconfirmedMessage(u *bridges.Unconfirmed) string {
	msg := "Bridge " + u.Bridge.Name + " was removed here, but the tailnet did not confirm within " +
		u.Wait.String() + ".\n\nThe device " + bridges.MachineName(u.Bridge.ID)
	if name := m.machines.Tailnet(u.Bridge); name != "" {
		msg += " may still be on " + name
	} else {
		msg += " may still be registered"
	}
	return msg + ". Delete it from the Tailscale admin console if it is."
}

// afterRemoval puts the user back on a list that no longer shows what they
// removed. A removal of the endpoint the failure screen is about leaves that
// screen with nothing to retry, so the root menu takes its place.
func (m *model) afterRemoval(ep config.Endpoint) tea.Cmd {
	if ep != nil && m.failedEndpoint == ep {
		m.clearEndpointFailure()
		m.resetStack(m.rootMenu())
		return tea.ClearScreen
	}
	// By title, and Bridges before the picker: both pages delete the same
	// thing, and refreshing the picker from the bridges page would replace the
	// page the user is on with a different list.
	for _, entry := range m.stack {
		if entry.Title == bridgesTitle {
			m.refreshBridgesMenu()
			return tea.ClearScreen
		}
	}
	m.refreshEndpointsMenu()
	return tea.ClearScreen
}
