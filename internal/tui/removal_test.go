package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
)

// withFakeDestroy stands in for the tailnet round trip a removal makes.
func withFakeDestroy(t *testing.T, fn func(context.Context, config.Bridge) error) {
	t.Helper()
	orig := destroyBridge
	destroyBridge = func(ctx context.Context, _ *bridges.Machines, b config.Bridge, _ func(connection.Event)) error {
		return fn(ctx, b)
	}
	t.Cleanup(func() { destroyBridge = orig })
}

// startedBridge creates the state directory tsnet would have made, which is
// what says this bridge registered a device.
func startedBridge(t *testing.T, id string) {
	t.Helper()
	dir, err := config.BridgeStateDir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func hasBridge(m *model, id string) bool {
	_, ok := m.g.Bridge(id)
	return ok
}

// removeRowResult drives a picker row's removal through its confirmation and
// returns the message the destruction produced.
func removeRowResult(t *testing.T, m *model, row connectionRow) tea.Msg {
	t.Helper()
	res := m.removeRow(row)
	if res.Next == nil {
		t.Fatalf("removing %s did not confirm before destroying its machine", row.bridge.Name)
	}
	_, item := findItem(t, res.Next.Items, "Remove")
	_, cmd := m.applyResult(item.Action())
	return activationResult(t, cmd)
}

// bridgedRow is the picker row for the saved endpoint behind Work, the one
// bridged connection pickerModel has.
func bridgedRow(t *testing.T, m *model) connectionRow {
	t.Helper()
	for _, row := range m.connectionRows() {
		if row.saved && !row.active && row.bridge.ID == "bridge-aaaaaa" {
			return row
		}
	}
	t.Fatal("no removable bridged row")
	return connectionRow{}
}

func TestRemovingTheLastConnectionDestroysTheMachineFirst(t *testing.T) {
	m := pickerModel(t)
	withFakeClients(t, []clients.Client{})
	startedBridge(t, "bridge-aaaaaa")
	row := bridgedRow(t, m)
	destroyed := 0
	withFakeDestroy(t, func(_ context.Context, b config.Bridge) error {
		destroyed++
		if b.ID != "bridge-aaaaaa" {
			t.Errorf("destroyed %s", b.ID)
		}
		// Settings are the only record naming the device, so they have to
		// outlast the logout.
		if !m.endpointConfigured(row.ep) || !hasBridge(m, b.ID) {
			t.Error("settings dropped before the machine was destroyed")
		}
		return nil
	})
	m.resetStack(m.endpointsMenu())

	msg := removeRowResult(t, m, row)
	if destroyed != 1 {
		t.Fatalf("destroy calls = %d, want the machine deregistered once", destroyed)
	}
	m.Update(msg)
	if m.endpointConfigured(row.ep) || hasBridge(m, "bridge-aaaaaa") {
		t.Errorf("records survived the removal: %+v %+v", m.g.Settings.Endpoints, m.g.Settings.Bridges)
	}
	saved, err := config.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Bridges) != 1 || saved.Bridges[0].ID != "bridge-bbbbbb" {
		t.Errorf("saved bridges = %+v, want only the untouched one", saved.Bridges)
	}
}

// A logout the tailnet refuses leaves a device the CLI must still be able to
// name, so nothing local goes.
func TestFailedDestroyKeepsTheConnection(t *testing.T) {
	m := pickerModel(t)
	withFakeClients(t, []clients.Client{})
	startedBridge(t, "bridge-aaaaaa")
	row := bridgedRow(t, m)
	withFakeDestroy(t, func(context.Context, config.Bridge) error { return errors.New("control plane said no") })
	m.resetStack(m.endpointsMenu())

	m.Update(removeRowResult(t, m, row))
	if !m.endpointConfigured(row.ep) || !hasBridge(m, "bridge-aaaaaa") {
		t.Errorf("failed removal dropped local records: %+v %+v", m.g.Settings.Endpoints, m.g.Settings.Bridges)
	}
	if m.step != stepError || !strings.Contains(m.errMsg, "control plane said no") {
		t.Errorf("step=%v errMsg=%q, want the refusal reported", m.step, m.errMsg)
	}
}

// A bridge nobody finished a login for has no device and no state directory.
// Confirming a tailnet round trip that cannot happen is a lie.
func TestRemovingAnUnstartedBridgeIsImmediate(t *testing.T) {
	m := pickerModel(t)
	withFakeClients(t, []clients.Client{})
	withFakeDestroy(t, func(_ context.Context, b config.Bridge) error {
		t.Errorf("logged out a bridge that never started: %s", b.ID)
		return nil
	})
	var row connectionRow
	for _, r := range m.connectionRows() {
		if !r.saved && r.bridge.ID == "bridge-bbbbbb" {
			row = r
		}
	}
	if row.bridge.ID == "" {
		t.Fatal("no bare bridge row")
	}
	m.resetStack(m.endpointsMenu())

	if res := m.removeRow(row); res.Next != nil {
		t.Error("confirmed a removal with nothing to remove from a tailnet")
	}
	if hasBridge(m, "bridge-bbbbbb") {
		t.Errorf("bridge survived: %+v", m.g.Settings.Bridges)
	}
}

// The bridges page deletes the same thing the picker does, so it has to reach
// the same destruction rather than dropping the record on its own.
func TestBridgesMenuDeleteDestroysTheMachine(t *testing.T) {
	m := pickerModel(t)
	withFakeClients(t, []clients.Client{})
	startedBridge(t, "bridge-bbbbbb")
	destroyed := 0
	withFakeDestroy(t, func(context.Context, config.Bridge) error { destroyed++; return nil })
	m.resetStack(m.bridgesMenu())
	// Row 0 is the explainer; Home is the bridge no endpoint points at.
	m.setCursor(2)

	_, item := findItem(t, m.top().Items, "delete")
	res := item.Action()
	if res.Next == nil {
		t.Fatal("deleting a bridge did not confirm")
	}
	_, confirm := findItem(t, res.Next.Items, "Remove")
	_, cmd := m.applyResult(confirm.Action())
	m.Update(activationResult(t, cmd))
	if destroyed != 1 || hasBridge(m, "bridge-bbbbbb") {
		t.Errorf("destroy calls = %d, bridges = %+v", destroyed, m.g.Settings.Bridges)
	}
}

// A bridge an endpoint still reaches through is not the picker's to delete
// from under it, and saying nothing reads as a key that does not work.
func TestBridgesMenuDeleteRefusesAReferencedBridge(t *testing.T) {
	m := pickerModel(t)
	withFakeClients(t, []clients.Client{})
	startedBridge(t, "bridge-aaaaaa")
	withFakeDestroy(t, func(_ context.Context, b config.Bridge) error {
		t.Errorf("destroyed %s while an endpoint still used it", b.ID)
		return nil
	})
	m.resetStack(m.bridgesMenu())
	m.setCursor(1)

	_, item := findItem(t, m.top().Items, "delete")
	m.Update(activationResult(t, item.Action().Cmd))
	if m.step != stepError || !strings.Contains(m.errMsg, config.DefaultLocation) {
		t.Errorf("step=%v errMsg=%q, want the endpoint holding the bridge named", m.step, m.errMsg)
	}
	if !hasBridge(m, "bridge-aaaaaa") {
		t.Error("referenced bridge was removed")
	}
}

// Point 6 of ADR 0002: the wait is bounded, and what survives it is named.
func TestDestroyTimeoutRemovesLocallyAndNamesTheDevice(t *testing.T) {
	m := pickerModel(t)
	withFakeClients(t, []clients.Client{})
	startedBridge(t, "bridge-aaaaaa")
	withFakeDestroy(t, func(_ context.Context, b config.Bridge) error {
		return &bridges.Unconfirmed{Bridge: b, Wait: 50 * time.Millisecond, Err: context.DeadlineExceeded}
	})
	row := bridgedRow(t, m)
	m.resetStack(m.endpointsMenu())

	m.Update(removeRowResult(t, m, row))
	if m.endpointConfigured(row.ep) || hasBridge(m, row.bridge.ID) {
		t.Errorf("timed-out removal kept local records: %+v", m.g.Settings)
	}
	for _, want := range []string{bridges.MachineName(row.bridge.ID), "corp.example.com"} {
		if !strings.Contains(m.errMsg, want) {
			t.Errorf("message %q does not name %q", m.errMsg, want)
		}
	}
}

// Ctrl+C during a removal used to close the Machines, which cancelled the
// destroy, and could quit before the outcome dropped the records: the next
// run then named a device that was already gone. Quitting waits for the
// outcome to be applied.
func TestQuitDuringRemovalWaitsForTheOutcome(t *testing.T) {
	m := pickerModel(t)
	withFakeClients(t, []clients.Client{})
	startedBridge(t, "bridge-aaaaaa")
	release := make(chan struct{})
	withFakeDestroy(t, func(context.Context, config.Bridge) error { <-release; return nil })
	row := bridgedRow(t, m)
	m.resetStack(m.endpointsMenu())

	res := m.removeRow(row)
	_, item := findItem(t, res.Next.Items, "Remove")
	_, destroy := m.applyResult(item.Action())
	result := make(chan tea.Msg, 1)
	go func() { result <- activationResult(t, destroy) }()

	_, quit := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quit != nil {
		if _, quitting := quit().(quitMsg); quitting {
			t.Fatal("Ctrl+C quit while the removal was still on the tailnet")
		}
	}
	close(release)
	_, after := m.Update(<-result)
	if m.endpointConfigured(row.ep) || hasBridge(m, row.bridge.ID) {
		t.Errorf("records survived the removal: %+v", m.g.Settings)
	}
	if after == nil {
		t.Fatal("no quit after the removal the user asked to leave during")
	}
	if _, quitting := activationResult(t, after).(quitMsg); !quitting {
		t.Error("the deferred quit did not happen once the outcome was applied")
	}
}
