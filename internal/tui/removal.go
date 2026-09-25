package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
	"github.com/tailscale/aperture-cli/internal/menu"
)

// bridgeRemovedMsg carries a Destroy's outcome back to the update loop, where
// settings may be written. endpoint is nil for a bare bridge.
type bridgeRemovedMsg struct {
	id       int
	bridge   config.Bridge
	endpoint config.Endpoint
	err      error
}

// destroyBridge logs the bridge's device out of its tailnet. A variable so
// tests can replace it: pickerModel has no Machines, and a real Destroy needs
// a tailnet.
var destroyBridge = func(ctx context.Context, machines *bridges.Machines, bridge config.Bridge, emit func(connection.Event)) error {
	return machines.Destroy(ctx, bridge, emit)
}

// removeRow removes the endpoint and bridge a picker row stands for. The
// row's page and the "d" key both call it, so they agree on what removing a
// row means.
func (m *model) removeRow(row connectionRow) menu.Result {
	var ep config.Endpoint
	if row.saved {
		ep = row.ep
	}
	return m.remove(row.bridge, ep)
}

// bridgeOf returns the bridge a saved endpoint connects through, or a zero
// Bridge for a direct endpoint. The setup guide holds an endpoint rather than
// a picker row.
func (m *model) bridgeOf(ep config.Endpoint) config.Bridge {
	if bridged, ok := ep.(config.BridgeEndpoint); ok {
		bridge, _ := m.g.Bridge(bridged.BridgeID())
		return bridge
	}
	return config.Bridge{}
}

// remove asks for confirmation when removing bridge or ep logs a device out
// of a tailnet, and otherwise deletes the records at once. Every delete in the
// TUI comes through here: the device outlives settings, so a site that skips
// this leaves a device on the user's tailnet that nothing names any more.
func (m *model) remove(bridge config.Bridge, ep config.Endpoint) menu.Result {
	if err := bridges.CheckRemovable(m.g, bridge, ep); err != nil {
		return errResult(err.Error())
	}
	if !bridges.WillDestroyMachine(m.g, bridge, ep) {
		if err := bridges.RemoveFromSettings(m.g, bridge, ep); err != nil {
			return errResult(err.Error())
		}
		return menu.Result{Cmd: m.afterRemoval(ep)}
	}
	return menu.Result{Next: m.removeBridgeMenu(bridge, ep)}
}

// removeBridgeMenu asks the user to confirm. Removal is irreversible from
// here and logs a device out of the user's tailnet — one per slot a process
// has claimed — so the screen names the devices the way the admin console
// does.
func (m *model) removeBridgeMenu(bridge config.Bridge, ep config.Endpoint) *menu.Menu {
	preamble := "Bridge " + bridge.Name + " is " + devicePhrase(bridge.ID)
	if name := m.machines.Tailnet(bridge); name != "" {
		preamble += " on " + name
	}
	preamble += ".\n\nRemoving it logs those devices out of the tailnet and discards the logins stored on this machine. " +
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

// destroyBridgeCmd runs the logout on the connect screen, where this program
// already shows slow bridge work and its log tail. The activation carries no
// cancel handle: settings still name the device, and abandoning the wait half
// way through a logout is how the record and the device end up disagreeing.
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

// bridgeRemoved applies a Destroy's outcome. The records go only after the
// device is gone. Any failure keeps the connection and shows why, so the
// user can retry; a quit deferred by Ctrl+C is dropped so the message stays
// on screen.
func (m *model) bridgeRemoved(msg bridgeRemovedMsg) (tea.Model, tea.Cmd) {
	if m.act == nil || m.act.id != msg.id {
		return m, nil
	}
	m.act = nil
	m.step = stepMenu
	err := msg.err
	if err == nil {
		err = bridges.RemoveFromSettings(m.g, msg.bridge, msg.endpoint)
	}
	if err != nil {
		m.quitAfterRemoval = false
		m.step = stepError
		m.errMsg = m.removalFailedMessage(msg.bridge, err)
		return m, nil
	}
	if m.quitAfterRemoval {
		m.quitAfterRemoval = false
		return m, m.quitCmd()
	}
	return m, m.afterRemoval(msg.endpoint)
}

// removalFailedMessage tells the user the connection is unchanged and how to
// finish the job: retry here, or delete the devices by name in the admin
// console. A bare error leaves them hunting for machines whose names this
// program chose.
func (m *model) removalFailedMessage(bridge config.Bridge, err error) string {
	msg := "Could not remove bridge " + bridge.Name + ": " + err.Error() +
		"\n\nThe connection is unchanged. Removing it again retries the logout. " +
		"If " + devicePhrase(bridge.ID)
	if name := m.machines.Tailnet(bridge); name != "" {
		msg += " is still on " + name
	} else {
		msg += " is still registered"
	}
	return msg + " after that, delete them from the Tailscale admin console."
}

// devicePhrase names the bridge's devices the way the admin console does.
// The names come from the state directories on disk; a bridge whose
// directories vanished mid-removal still names its first slot, the device a
// retry would go and find.
func devicePhrase(bridgeID string) string {
	names, err := bridges.MachineNames(bridgeID)
	if err != nil || len(names) == 0 {
		return "the device " + bridges.MachineName(bridgeID, 1)
	}
	if len(names) == 1 {
		return "the device " + names[0]
	}
	return "the devices " + strings.Join(names, ", ")
}

// afterRemoval returns the user to a list that no longer shows what they
// removed. When the removed endpoint is the one the failure screen is about,
// that screen has nothing left to retry, so the root menu takes its place.
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
