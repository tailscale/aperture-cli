package bridges

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

// isolateConfig points the bridge state directories at a per-test throwaway
// config dir, on every platform os.UserConfigDir reads.
func isolateConfig(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("APPDATA", tmp)
}

// APT-330: two aperture processes opening the same bridge must not open the
// same state directory. One directory is one node key, and the control plane
// hands the node to the process that registered last, leaving every earlier
// session dialed into a dead node.
func TestConcurrentProcessesGetDistinctSlots(t *testing.T) {
	isolateConfig(t)
	bridge := config.Bridge{ID: "bridge-aaaa01", Name: "Work"}
	var mu sync.Mutex
	var dirs []string
	newNode := func(_ config.Bridge, _ int, dir string, _, _ func(string, ...any)) tailnetNode {
		mu.Lock()
		dirs = append(dirs, dir)
		mu.Unlock()
		return &fakeNode{}
	}
	// Two Machines collections stand in for two aperture processes.
	p1 := NewMachines(false)
	p1.newNode = newNode
	defer p1.Close()
	p2 := NewMachines(false)
	p2.newNode = newNode
	defer p2.Close()

	m1, err := p1.For(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := m1.Open(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	m2, err := p2.For(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := m2.Open(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 {
		t.Fatalf("nodes constructed = %d, want 2", len(dirs))
	}
	if dirs[0] == dirs[1] {
		t.Fatalf("both processes opened %s: one node key, the second registration evicts the first (APT-330)", dirs[0])
	}
}

// A closed Machine frees its slot, so the next process to open the bridge
// reuses slot 1 rather than minting a new device on the tailnet.
func TestClosedMachineReleasesItsSlot(t *testing.T) {
	isolateConfig(t)
	bridge := config.Bridge{ID: "bridge-bbbb02", Name: "Work"}
	var mu sync.Mutex
	var dirs []string
	newNode := func(_ config.Bridge, _ int, dir string, _, _ func(string, ...any)) tailnetNode {
		mu.Lock()
		dirs = append(dirs, dir)
		mu.Unlock()
		return &fakeNode{}
	}

	p1 := NewMachines(false)
	p1.newNode = newNode
	m1, err := p1.For(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := m1.Open(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := p1.Close(); err != nil {
		t.Fatal(err)
	}

	p2 := NewMachines(false)
	p2.newNode = newNode
	defer p2.Close()
	m2, err := p2.For(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := m2.Open(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 {
		t.Fatalf("nodes constructed = %d, want 2", len(dirs))
	}
	if dirs[0] != dirs[1] {
		t.Fatalf("second process opened %s after the first closed %s: slot was not released", dirs[1], dirs[0])
	}
}

func TestMachineNameNumbersSlotsFromTwo(t *testing.T) {
	first := MachineName("bridge-abcdef", 1)
	if first != "aperture-cli-bridge-abcdef" {
		t.Errorf("MachineName(slot 1) = %q, want the name existing devices already have", first)
	}
	third := MachineName("bridge-abcdef", 3)
	if third != "aperture-cli-bridge-abcdef-3" {
		t.Errorf("MachineName(slot 3) = %q, want aperture-cli-bridge-abcdef-3", third)
	}
}

// Destroy logs out every slot the bridge has on disk, not only the one this
// process ran, and removes every slot's state directory.
func TestDestroyLogsOutEverySlot(t *testing.T) {
	isolateConfig(t)
	bridge := config.Bridge{ID: "bridge-cccc03", Name: "Work"}
	var nodes []*fakeNode
	newNode := func(_ config.Bridge, _ int, _ string, _, _ func(string, ...any)) tailnetNode {
		n := &fakeNode{}
		nodes = append(nodes, n)
		return n
	}
	// Two processes ran the bridge at once, leaving two slots on disk. The
	// state directory is what tsnet would have created on start; the fake
	// node never makes one.
	var dirs []string
	var past []*Machines
	for range 2 {
		p := NewMachines(false)
		p.newNode = func(b config.Bridge, slot int, dir string, u, d func(string, ...any)) tailnetNode {
			dirs = append(dirs, dir)
			return newNode(b, slot, dir, u, d)
		}
		mc, err := p.For(bridge)
		if err != nil {
			t.Fatal(err)
		}
		if err := mc.Open(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dirs[len(dirs)-1], 0o700); err != nil {
			t.Fatal(err)
		}
		past = append(past, p)
	}
	if dirs[0] == dirs[1] {
		t.Fatalf("both processes opened %s", dirs[0])
	}
	for _, p := range past {
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if !HasMachine(bridge.ID) {
		t.Fatal("two slots opened, HasMachine = false")
	}

	destroyer := NewMachines(false)
	destroyer.newNode = newNode
	defer destroyer.Close()
	if err := destroyer.Destroy(context.Background(), bridge, nil); err != nil {
		t.Fatal(err)
	}

	loggedOut := 0
	for _, n := range nodes {
		loggedOut += n.loggedOut
	}
	if loggedOut != 2 {
		t.Errorf("logouts across slots = %d, want 2", loggedOut)
	}
	if HasMachine(bridge.ID) {
		t.Error("state directories survived Destroy")
	}
}

// Destroy refuses while another process holds a slot: logging its node out
// from under it is the eviction this change exists to stop.
func TestDestroyRefusesSlotHeldByAnotherProcess(t *testing.T) {
	isolateConfig(t)
	bridge := config.Bridge{ID: "bridge-dddd04", Name: "Work"}
	newNode := func(_ config.Bridge, _ int, _ string, _, _ func(string, ...any)) tailnetNode {
		return &fakeNode{}
	}
	p1 := NewMachines(false)
	p1.newNode = newNode
	defer p1.Close()
	mc, err := p1.For(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := mc.Open(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// The state directory tsnet would have created on start.
	dir, err := config.BridgeStateDir(bridge.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	p2 := NewMachines(false)
	p2.newNode = newNode
	defer p2.Close()
	err = p2.Destroy(context.Background(), bridge, nil)
	if err == nil || !strings.Contains(err.Error(), "another aperture process") {
		t.Fatalf("Destroy error = %v, want refusal naming the process still using the bridge", err)
	}
	if !HasMachine(bridge.ID) {
		t.Error("the refused Destroy still removed state")
	}
}
