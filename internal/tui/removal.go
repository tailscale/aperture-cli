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
// update loop, where the records can be dropped.
type bridgeRemovedMsg struct {
	id      int
	removal bridges.Removal
	err     error
}

// destroyBridge is the tailnet round trip a removal makes. A seam for the
// tests: pickerModel has no Machines, and a real Destroy would want a tailnet.
var destroyBridge = func(ctx context.Context, b bridges.Bridging, rem bridges.Removal, emit func(connection.Event)) error {
	return b.Destroy(ctx, rem, emit)
}

// removeRow deletes what a picker row stands for. Shared by the row's page and
// the "d" key, which have to agree on what removing a row means.
func (m *model) removeRow(row connectionRow) menu.Result {
	rem := bridges.Removal{Bridge: row.bridge}
	if row.saved {
		ep := row.ep
		rem.Endpoint = &ep
	}
	return m.remove(rem)
}

// removalFor is the removal a saved endpoint implies, bridge included. The
// setup guide holds an endpoint rather than a picker row.
func (m *model) removalFor(ep config.Endpoint) bridges.Removal {
	rem := bridges.Removal{Endpoint: &ep}
	rem.Bridge, _ = m.g.Bridge(ep.BridgeID)
	return rem
}

// remove confirms before a removal that takes a device off a tailnet, and
// otherwise drops the records at once. Every delete in the TUI comes through
// here: the machine outlives settings, so a site that skips this leaves a
// device on the user's tailnet that nothing names any more.
func (m *model) remove(rem bridges.Removal) menu.Result {
	destroys, err := m.bridging().Destroys(rem)
	if err != nil {
		return errResult(err.Error())
	}
	if !destroys {
		if err := m.bridging().Forget(rem, nil); err != nil {
			return errResult(err.Error())
		}
		return menu.Result{Cmd: m.afterRemoval(rem)}
	}
	return menu.Result{Next: m.removeBridgeMenu(rem)}
}

// removeBridgeMenu is the confirmation. Removal is irreversible from here and
// takes a device off the user's tailnet, so the screen names the device by the
// name the admin console shows it under.
func (m *model) removeBridgeMenu(rem bridges.Removal) *menu.Menu {
	preamble := "Bridge " + rem.Bridge.Name + " is the device " + bridges.MachineName(rem.Bridge.ID)
	if name := m.bridging().Tailnet(rem.Bridge); name != "" {
		preamble += " on " + name
	}
	preamble += ".\n\nRemoving it logs that device out of the tailnet and discards the login stored on this machine. " +
		"Connecting through a bridge of this name again is a new device and a new login."
	return &menu.Menu{
		Title:    "Remove bridge " + rem.Bridge.Name + "?",
		Preamble: preamble,
		Items: []menu.MenuItem{
			{
				Label:    "Remove",
				Shortcut: "y",
				Action:   func() menu.Result { return menu.Result{Cmd: m.destroyBridgeCmd(rem)} },
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
func (m *model) destroyBridgeCmd(rem bridges.Removal) tea.Cmd {
	m.stopActivation()
	m.step = stepPreflight
	m.preflightErr = ""
	m.bridgeLogs = nil

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan bridgeLine, 32)
	m.activationSeq++
	act := &activation{
		id:      m.activationSeq,
		label:   "Removing bridge " + rem.Bridge.Name + " ...",
		started: time.Now(),
		logCh:   ch,
		logCtx:  ctx,
	}
	m.act = act
	emit := bridgeLogSink(ctx, ch, act.started)
	bridging := m.bridging()
	destroy := func() tea.Msg {
		defer cancel()
		err := destroyBridge(ctx, bridging, rem, emit)
		return bridgeRemovedMsg{id: act.id, removal: rem, err: err}
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
	err := m.bridging().Forget(msg.removal, msg.err)
	var unconfirmed *bridges.Unconfirmed
	switch {
	case errors.As(err, &unconfirmed):
		cmd := m.afterRemoval(msg.removal)
		m.step = stepError
		m.errMsg = m.unconfirmedMessage(unconfirmed)
		return m, cmd
	case err != nil && errors.Is(err, msg.err):
		m.step = stepError
		m.errMsg = "Could not remove bridge " + msg.removal.Bridge.Name + ": " + err.Error() +
			"\n\nThe connection is unchanged. Removing it again retries the logout."
		return m, nil
	case err != nil:
		m.step = stepError
		m.errMsg = err.Error()
		return m, nil
	}
	return m, m.afterRemoval(msg.removal)
}

// unconfirmedMessage is what the user needs to finish the job by hand: the
// device name, and where to look for it. A bare "timed out" leaves them
// hunting for a machine whose name this program chose.
func (m *model) unconfirmedMessage(u *bridges.Unconfirmed) string {
	msg := "Bridge " + u.Bridge.Name + " was removed here, but the tailnet did not confirm within " +
		u.Wait.String() + ".\n\nThe device " + bridges.MachineName(u.Bridge.ID)
	if name := m.bridging().Tailnet(u.Bridge); name != "" {
		msg += " may still be on " + name
	} else {
		msg += " may still be registered"
	}
	return msg + ". Delete it from the Tailscale admin console if it is."
}

// afterRemoval puts the user back on a list that no longer shows what they
// removed. A removal of the endpoint the failure screen is about leaves that
// screen with nothing to retry, so the root menu takes its place.
func (m *model) afterRemoval(rem bridges.Removal) tea.Cmd {
	if rem.Endpoint != nil && m.failedEndpoint != nil && config.SameEndpoint(*m.failedEndpoint, *rem.Endpoint) {
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
