package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
	"github.com/tailscale/aperture-cli/internal/menu"
)

// bridgeRemoval is what one delete is about: the endpoint the user picked, and
// the bridge that endpoint was the last reason to keep. Either can be absent.
type bridgeRemoval struct {
	bridge   config.Bridge
	endpoint *config.Endpoint
}

// bridgeRemovedMsg carries the outcome of the tailnet round trip back to the
// update loop. timedOut separates "the tailnet refused" from "the tailnet did
// not answer in time", which are opposite answers about the local records.
type bridgeRemovedMsg struct {
	id       int
	removal  bridgeRemoval
	err      error
	timedOut bool
}

// Seams for the tests: pickerModel has no bridge manager, and a real Destroy
// would want a tailnet.
var (
	destroyBridge = func(ctx context.Context, mgr *bridges.Manager, bridge config.Bridge, emit func(connection.Event)) error {
		return mgr.Destroy(ctx, bridge, emit)
	}
	bridgeHasMachine = bridges.HasMachine
)

// bridgeDestroyTimeout bounds the logout. /machine/register was hanging past 90
// seconds on 2026-09-17 and logout is a round trip to the same place, so the
// delete cannot wait on it indefinitely (ADR 0002, decision 6).
var bridgeDestroyTimeout = 45 * time.Second

// removeRow deletes what a picker row stands for. Shared by the row's page and
// the "d" key, which have to agree on what removing a row means.
func (m *model) removeRow(row connectionRow) menu.Result {
	if row.active {
		return errResult("connect to another endpoint before removing the active one")
	}
	rem := bridgeRemoval{bridge: row.bridge}
	if row.saved {
		ep := row.ep
		rem.endpoint = &ep
	}
	return m.remove(rem)
}

// removalFor is the removal a saved endpoint implies, bridge included. The
// setup guide holds an endpoint rather than a picker row.
func (m *model) removalFor(ep config.Endpoint) bridgeRemoval {
	rem := bridgeRemoval{endpoint: &ep}
	rem.bridge, _ = m.g.Bridge(ep.BridgeID)
	return rem
}

// remove confirms and destroys the bridge's machine when this is the last
// reference to it, and otherwise just drops the records. Every delete in the
// TUI comes through here: the machine outlives settings, so a site that skips
// this leaves a device on the user's tailnet that nothing names any more.
func (m *model) remove(rem bridgeRemoval) menu.Result {
	if rem.endpoint == nil {
		if ep, used := m.endpointUsing(rem.bridge.ID); used {
			return errResult("bridge " + rem.bridge.Name + " is used by endpoint " + ep.URL + "; remove that connection instead")
		}
	}
	if !m.destroys(rem) {
		if err := m.removeRecords(rem); err != nil {
			return errResult(err.Error())
		}
		return menu.Result{Cmd: m.afterRemoval(rem)}
	}
	return menu.Result{Next: m.removeBridgeMenu(rem)}
}

// destroys reports whether this removal takes the bridge's last endpoint and
// leaves a machine behind. A bridge that never started has no device, and must
// not start one to find out: bring-up is the interactive login being removed.
func (m *model) destroys(rem bridgeRemoval) bool {
	if rem.bridge.ID == "" || !bridgeHasMachine(rem.bridge.ID) {
		return false
	}
	for _, ep := range m.g.Settings.Endpoints {
		if ep.BridgeID != rem.bridge.ID {
			continue
		}
		if rem.endpoint == nil || !sameEndpoint(ep, *rem.endpoint) {
			return false
		}
	}
	return true
}

func (m *model) endpointUsing(bridgeID string) (config.Endpoint, bool) {
	for _, ep := range m.g.Settings.Endpoints {
		if bridgeID != "" && ep.BridgeID == bridgeID {
			return ep, true
		}
	}
	return config.Endpoint{}, false
}

// removeBridgeMenu is the confirmation. Removal is irreversible from here and
// takes a device off the user's tailnet, so the screen names the device by the
// name the admin console shows it under.
func (m *model) removeBridgeMenu(rem bridgeRemoval) *menu.Menu {
	preamble := "Bridge " + rem.bridge.Name + " is the device " + bridges.MachineName(rem.bridge.ID)
	if name := m.bridgeTailnet(rem.bridge); name != "" {
		preamble += " on " + name
	}
	preamble += ".\n\nRemoving it logs that device out of the tailnet and discards the login stored on this machine. " +
		"Connecting through a bridge of this name again is a new device and a new login."
	return &menu.Menu{
		Title:    "Remove bridge " + rem.bridge.Name + "?",
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
func (m *model) destroyBridgeCmd(rem bridgeRemoval) tea.Cmd {
	m.stopActivation()
	m.step = stepPreflight
	m.preflightErr = ""
	m.bridgeLogs = nil

	ctx, cancel := context.WithTimeout(context.Background(), bridgeDestroyTimeout)
	ch := make(chan bridgeLine, 32)
	m.activationSeq++
	act := &activation{
		id:      m.activationSeq,
		label:   "Removing bridge " + rem.bridge.Name + " ...",
		started: time.Now(),
		logCh:   ch,
		logCtx:  ctx,
	}
	m.act = act
	emit := bridgeLogSink(ctx, ch, act.started)
	destroy := func() tea.Msg {
		defer cancel()
		err := destroyBridge(ctx, m.bridgeManager, rem.bridge, emit)
		return bridgeRemovedMsg{id: act.id, removal: rem, err: err, timedOut: err != nil && ctx.Err() != nil}
	}
	return tea.Batch(destroy, waitBridgeLog(ctx, ch), activationTick(act.id))
}

// bridgeRemoved applies the outcome. Settings go only once the device is gone
// or is known to have outlived the wait, because settings are the only record
// that the device exists.
func (m *model) bridgeRemoved(msg bridgeRemovedMsg) (tea.Model, tea.Cmd) {
	if m.act == nil || m.act.id != msg.id {
		return m, nil
	}
	m.act = nil
	m.step = stepMenu
	if msg.err != nil && !msg.timedOut {
		m.step = stepError
		m.errMsg = "Could not remove bridge " + msg.removal.bridge.Name + ": " + msg.err.Error() +
			"\n\nThe connection is unchanged. Removing it again retries the logout."
		return m, nil
	}
	if err := m.removeRecords(msg.removal); err != nil {
		m.step = stepError
		m.errMsg = err.Error()
		return m, nil
	}
	cmd := m.afterRemoval(msg.removal)
	if msg.timedOut {
		m.step = stepError
		m.errMsg = m.timedOutMessage(msg.removal.bridge)
	}
	return m, cmd
}

// timedOutMessage is what the user needs to finish the job by hand: the device
// name, and where to look for it. A bare "timed out" leaves them hunting for a
// machine whose name this program chose.
func (m *model) timedOutMessage(bridge config.Bridge) string {
	msg := "Bridge " + bridge.Name + " was removed here, but the tailnet did not confirm within " +
		bridgeDestroyTimeout.String() + ".\n\nThe device " + bridges.MachineName(bridge.ID)
	if name := m.bridgeTailnet(bridge); name != "" {
		msg += " may still be on " + name
	} else {
		msg += " may still be registered"
	}
	return msg + ". Delete it from the Tailscale admin console if it is."
}

// removeRecords drops the settings this removal covers, endpoint first: a
// bridge an endpoint still points at cannot be removed (global.go RemoveBridge).
func (m *model) removeRecords(rem bridgeRemoval) error {
	if rem.endpoint != nil {
		for i, ep := range m.g.Settings.Endpoints {
			if i == 0 || !sameEndpoint(ep, *rem.endpoint) {
				continue
			}
			if err := m.g.RemoveEndpoint(i); err != nil {
				return err
			}
			break
		}
	}
	return m.dropOrphanBridge(rem.bridge.ID)
}

// afterRemoval puts the user back on a list that no longer shows what they
// removed. A removal of the endpoint the failure screen is about leaves that
// screen with nothing to retry, so the root menu takes its place.
func (m *model) afterRemoval(rem bridgeRemoval) tea.Cmd {
	if rem.endpoint != nil && m.failedEndpoint != nil && sameEndpoint(*m.failedEndpoint, *rem.endpoint) {
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
