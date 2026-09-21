package bridges

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
)

const (
	bridgePeerWaitWindow   = 5 * time.Second
	bridgePeerWaitInterval = 250 * time.Millisecond
)

// Machines is the process's Machines, one per Bridge, and the only place a
// Machine is created: two Machines for one Bridge would open the same state
// directory. Getting a member does no network work. Close ends every member
// and refuses new ones.
type Machines struct {
	mu       sync.Mutex
	byBridge map[string]*Machine
	closed   bool
	shutdown func() error

	debug bool
	// peerWait bounds how long a dial waits for the target to appear in the
	// node's peer map before giving up and resolving it the way tsnet would.
	peerWait         time.Duration
	peerWaitInterval time.Duration
	newNode          func(bridge config.Bridge, stateDir string, userLogf, debugLogf func(string, ...any)) tailnetNode
}

// NewMachines returns an empty collection. When debug is true, verbose tsnet
// backend logs are also reported to whichever attempt is using a Machine.
func NewMachines(debug bool) *Machines {
	ms := &Machines{
		byBridge:         make(map[string]*Machine),
		debug:            debug,
		peerWait:         bridgePeerWaitWindow,
		peerWaitInterval: bridgePeerWaitInterval,
		newNode:          newTSNetNode(debug),
	}
	ms.shutdown = sync.OnceValue(ms.close)
	return ms
}

// For returns the Machine for bridge, creating an idle one on first use.
func (ms *Machines) For(bridge config.Bridge) (*Machine, error) {
	if ms == nil {
		return nil, errors.New("bridges are not configured")
	}
	if err := validateBridgeID(bridge.ID); err != nil {
		return nil, err
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.closed {
		return nil, net.ErrClosed
	}
	mc := ms.byBridge[bridge.ID]
	if mc == nil {
		mc = newMachine(bridge, ms)
		ms.byBridge[bridge.ID] = mc
	}
	return mc, nil
}

// lookup is For without the creation, for callers that only want to read a
// Machine that already exists.
func (ms *Machines) lookup(bridgeID string) *Machine {
	if ms == nil {
		return nil
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.byBridge[bridgeID]
}

// Close ends every Machine. Concurrent and subsequent callers wait for the
// same cleanup and receive the same result, so nobody reports completion while
// another caller is still tearing a Machine down.
func (ms *Machines) Close() error {
	if ms == nil || ms.shutdown == nil {
		return nil
	}
	return ms.shutdown()
}

func (ms *Machines) close() error {
	ms.mu.Lock()
	ms.closed = true
	members := slices.Collect(maps.Values(ms.byBridge))
	ms.mu.Unlock()
	var errs []error
	for _, mc := range members {
		errs = append(errs, mc.Close())
	}
	return errors.Join(errs...)
}

// validateBridgeID rejects IDs that don't match the system-generated
// "bridge-<hex>" format, so a hand-edited config can't inject arbitrary
// content into the tailnet hostname.
func validateBridgeID(id string) error {
	suffix, ok := strings.CutPrefix(id, "bridge-")
	if !ok || suffix == "" {
		return fmt.Errorf("invalid bridge ID %q", id)
	}
	if len(suffix) > 64 {
		return fmt.Errorf("invalid bridge ID %q", id)
	}
	for _, r := range suffix {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return fmt.Errorf("invalid bridge ID %q", id)
		}
	}
	return nil
}
