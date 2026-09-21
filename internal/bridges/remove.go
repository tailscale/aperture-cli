package bridges

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
)

// destroyTimeout bounds the logout a removal waits on. /machine/register was
// hanging past 90 seconds on 2026-09-17 and logout is a round trip to the same
// place, so a removal cannot wait on it indefinitely (ADR 0002, decision 6).
var destroyTimeout = 45 * time.Second

// CheckRemovable returns an error when endpoint, or the bridge itself when
// endpoint is nil, cannot be removed. The active endpoint stays: it is the
// connection the user falls back to. A bridge stays while an endpoint still
// connects through it.
func CheckRemovable(g *config.Global, bridge config.Bridge, endpoint config.Endpoint) error {
	if endpoint != nil && endpoint == g.ActiveEndpoint() {
		return errors.New("connect to another endpoint before removing the active one")
	}
	if endpoint == nil && bridge.ID != "" {
		if users := endpointsThroughBridge(g, bridge.ID); len(users) > 0 {
			return fmt.Errorf("bridge %s is used by endpoint %s; remove that connection instead", bridge.Name, users[0].URL())
		}
	}
	return nil
}

// WillDestroyMachine reports whether removing endpoint, or the bridge itself
// when endpoint is nil, logs a device out of a tailnet. It does when the
// bridge has started a Machine and no other endpoint connects through it. A
// bridge that never started has no device and must not start one to find out.
func WillDestroyMachine(g *config.Global, bridge config.Bridge, endpoint config.Endpoint) bool {
	if bridge.ID == "" || !HasMachine(bridge.ID) {
		return false
	}
	for _, other := range endpointsThroughBridge(g, bridge.ID) {
		if other != endpoint {
			return false
		}
	}
	return true
}

// Destroy logs the bridge's Machine out of its tailnet and discards its
// login, waiting at most destroyTimeout for the tailnet to answer. Destroy
// writes no settings. The caller removes the bridge's records only after
// Destroy returns nil: the records are the only thing naming the device, and
// a failed or timed-out logout must stay retryable (ADR 0002).
func (ms *Machines) Destroy(ctx context.Context, bridge config.Bridge, emit func(connection.Event)) error {
	mc, err := ms.For(bridge)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, destroyTimeout)
	defer cancel()
	err = mc.Destroy(ctx, emit)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("the tailnet did not answer within %s: %w", destroyTimeout, err)
	}
	return err
}

// Tailnet returns the tailnet name the bridge's running Machine reports,
// falling back to the name saved on the bridge. A bridge that switched
// tailnets this session keeps a stale saved name until the next verified
// connection rewrites it.
func (ms *Machines) Tailnet(bridge config.Bridge) string {
	if mc := ms.lookup(bridge.ID); mc != nil {
		if name := mc.Tailnet(); name != "" {
			return name
		}
	}
	return bridge.Tailnet
}

// RemoveFromSettings deletes endpoint from settings, then deletes bridge when
// no endpoint connects through it any more. Call it only after Destroy has
// returned nil, or when WillDestroyMachine is false.
//
// The endpoint and its bridge go together because the picker shows them as
// one row. Deleting the endpoint alone left the bridge listed as a bare
// "Connect via" row, so to the user the row moved instead of disappearing.
func RemoveFromSettings(g *config.Global, bridge config.Bridge, endpoint config.Endpoint) error {
	if err := CheckRemovable(g, bridge, endpoint); err != nil {
		return err
	}
	if endpoint != nil {
		if err := g.DropEndpoint(endpoint); err != nil {
			return err
		}
	}
	if bridge.ID != "" && len(endpointsThroughBridge(g, bridge.ID)) == 0 {
		return g.RemoveBridge(bridge.ID)
	}
	return nil
}

// endpointsThroughBridge returns every saved endpoint that connects through
// bridgeID.
func endpointsThroughBridge(g *config.Global, bridgeID string) []config.Endpoint {
	var users []config.Endpoint
	for _, endpoint := range g.Settings.Endpoints {
		if isReachableThroughBridge(endpoint, bridgeID) {
			users = append(users, endpoint)
		}
	}
	return users
}

// isReachableThroughBridge reports whether endpoint connects through bridgeID.
func isReachableThroughBridge(endpoint config.Endpoint, bridgeID string) bool {
	bridged, ok := endpoint.(config.BridgeEndpoint)
	return ok && bridged.BridgeID() == bridgeID
}
