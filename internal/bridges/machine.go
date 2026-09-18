package bridges

import (
	"context"
	"errors"
	"net"
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
