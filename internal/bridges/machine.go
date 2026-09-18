package bridges

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
)

// Machine owns the node and proxies for one bridge. Its turn covers an entire
// activation or logout, including cleanup; a cached Machine may have no node.
type Machine struct {
	node    tailnetNode
	proxies map[string]*proxyRuntime
	ev      *liveEvents
	turn    chan struct{}
	// cancel is guarded by Manager.mu, so shutdown can interrupt the owner
	// without waiting for its turn (which may be waiting for authorization).
	cancel context.CancelFunc
}

// acquire grants a cancellable turn on one Machine. Manager.mu only protects
// the cache and cancellation handles, never network work or turn acquisition.
func (m *Manager) acquire(ctx context.Context, bridgeID string) (context.Context, *Machine, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	if m.nodes == nil {
		m.mu.Unlock()
		return nil, nil, net.ErrClosed
	}
	rt := m.nodes[bridgeID]
	if rt == nil {
		rt = &Machine{
			proxies: make(map[string]*proxyRuntime),
			ev:      &liveEvents{},
			turn:    make(chan struct{}, 1),
		}
		m.nodes[bridgeID] = rt
	}
	m.mu.Unlock()
	select {
	case rt.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nodes == nil {
		<-rt.turn
		return nil, nil, net.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		<-rt.turn
		return nil, nil, err
	}
	ctx, rt.cancel = context.WithCancel(ctx)
	return ctx, rt, nil
}

func (m *Manager) release(rt *Machine) {
	m.mu.Lock()
	rt.cancel()
	rt.cancel = nil
	m.mu.Unlock()
	<-rt.turn
}

// MachineName is the hostname this bridge's node registers under, and so the
// device name the tailnet shows. Removal has to name the same thing the admin
// console does, or a user told to go delete it by hand cannot find it.
func MachineName(bridgeID string) string { return "aperture-cli-" + bridgeID }

// HasMachine reports whether this bridge ever started a node. tsnet creates
// the state directory on first use, so its absence is the only durable
// evidence that nothing was ever registered: Bridge.Tailnet is a display hint,
// saved after verification and cleared before a switch.
func HasMachine(bridgeID string) bool {
	dir, err := config.BridgeStateDir(bridgeID)
	if err != nil {
		return false
	}
	_, err = os.Stat(dir)
	return !errors.Is(err, fs.ErrNotExist)
}

// Destroy removes a bridge's machine from its tailnet and discards the state
// directory it kept the login in. It touches no settings: the Bridge record is
// the only thing naming the device, so the caller drops it after this returns
// nil and keeps it otherwise (ADR 0002).
//
// A bridge that never started has no device and must not start one to find
// out: bring-up is what would demand the interactive login being removed.
func (m *Manager) Destroy(ctx context.Context, bridge config.Bridge, emit func(connection.Event)) error {
	if m == nil {
		return fmt.Errorf("bridge manager is not configured")
	}
	if err := validateBridgeID(bridge.ID); err != nil {
		return err
	}
	stateDir, err := config.BridgeStateDir(bridge.ID)
	if err != nil {
		return err
	}
	ev := sink(emit)
	ctx, rt, err := m.acquire(ctx, bridge.ID)
	if err != nil {
		return err
	}
	defer m.release(rt)
	if rt.node == nil && !HasMachine(bridge.ID) {
		m.forget(bridge.ID)
		return nil
	}
	if err := m.initNode(bridge, rt, ev); err != nil {
		return err
	}
	ev.note("Removing bridge " + bridge.Name + " from its tailnet ...")
	err = rt.destroy(ctx, stateDir)
	m.forget(bridge.ID)
	if err != nil {
		return err
	}
	ev.note("Bridge " + bridge.Name + " is no longer a device on that tailnet.")
	return nil
}

// forget drops what this session learned about a bridge. The cache entry stays:
// it carries the turn, and a second Machine for one bridge could open the state
// directory a first one is still using.
func (m *Manager) forget(bridgeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tailnets, bridgeID)
}

// destroy deregisters this Machine and discards its persistence. Called with
// the Machine's turn held.
//
// Logout needs an initialized LocalAPI rather than an authorized node, so an
// identity the tailnet will no longer accept can still be removed. The state
// directory goes last and only on success: it holds the node key, which is
// what a later attempt would need to deregister the device.
func (rt *Machine) destroy(ctx context.Context, stateDir string) error {
	logoutErr := rt.node.Logout(ctx)
	closeErr := rt.close()
	if err := errors.Join(logoutErr, closeErr); err != nil {
		return err
	}
	return os.RemoveAll(stateDir)
}

// close is called with the Machine's turn held. It must finish before a new
// node can open the same bridge state directory.
func (rt *Machine) close() error {
	var errs []error
	for key, proxy := range rt.proxies {
		errs = append(errs, closeProxy(proxy))
		delete(rt.proxies, key)
	}
	if rt.node != nil {
		errs = append(errs, rt.node.Close())
		rt.node = nil
	}
	return errors.Join(errs...)
}
