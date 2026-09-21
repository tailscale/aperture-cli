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

// Unconfirmed is a removal the tailnet did not confirm within the wait. The
// local records are gone; the device may not be, and the user has to be told
// where to look for it.
type Unconfirmed struct {
	Bridge config.Bridge
	Wait   time.Duration
	Err    error
}

func (e *Unconfirmed) Error() string {
	return fmt.Sprintf("the tailnet did not confirm within %s: %v", e.Wait, e.Err)
}

func (e *Unconfirmed) Unwrap() error { return e.Err }

// DestroysMachine reports whether removing ep, or the bare bridge when ep is
// nil, takes a Machine off a tailnet: ep is the Bridge's last Endpoint and the
// Bridge has started a Machine. A Bridge that never started has no device, and
// must not start one to find out. An error means it may not be removed at all.
func DestroysMachine(g *config.Global, bridge config.Bridge, ep config.Endpoint) (bool, error) {
	if err := removable(g, bridge, ep); err != nil {
		return false, err
	}
	if bridge.ID == "" || !HasMachine(bridge.ID) {
		return false, nil
	}
	for _, other := range g.Settings.Endpoints {
		if through(other, bridge.ID) && other != ep {
			return false, nil
		}
	}
	return true, nil
}

// Destroy takes the bridge's Machine off its tailnet, waiting at most
// destroyTimeout for the tailnet to confirm. Settings are untouched:
// ForgetBridge drops them once the caller has the outcome, because they are
// the only record that the device exists.
func (ms *Machines) Destroy(ctx context.Context, bridge config.Bridge, emit func(connection.Event)) error {
	mc, err := ms.For(bridge)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, destroyTimeout)
	defer cancel()
	err = mc.Destroy(ctx, emit)
	if err != nil && ctx.Err() != nil {
		return &Unconfirmed{Bridge: bridge, Wait: destroyTimeout, Err: err}
	}
	return err
}

// Tailnet is the network a Bridge reaches, preferring what its running
// Machine reports to what was saved: a bridge that switched tailnets this
// session leaves a stale name on disk until the next verified connection
// rewrites it.
func (ms *Machines) Tailnet(bridge config.Bridge) string {
	if mc := ms.lookup(bridge.ID); mc != nil {
		if name := mc.Tailnet(); name != "" {
			return name
		}
	}
	return bridge.Tailnet
}

// ForgetBridge drops the records a removal covers, endpoint first: a Bridge
// an Endpoint still points at cannot be removed. destroyErr is Destroy's
// outcome, nil for a removal with nothing to destroy. A refusal keeps
// everything and is returned as is: the device is still on the tailnet and
// settings are the only thing naming it. A wait that expired drops the
// records and returns the *Unconfirmed, because the device may have outlived
// the wait.
//
// Settings hold two objects where the picker shows one row, so removing the
// endpoint alone left the bridge re-listed as a bare "Connect via" row: to the
// user the row moved instead of going. A bridge two endpoints reach through
// stays.
func ForgetBridge(g *config.Global, bridge config.Bridge, ep config.Endpoint, destroyErr error) error {
	var unconfirmed *Unconfirmed
	if destroyErr != nil && !errors.As(destroyErr, &unconfirmed) {
		return destroyErr
	}
	if err := removable(g, bridge, ep); err != nil {
		return err
	}
	if ep != nil {
		if err := g.DropEndpoint(ep); err != nil {
			return err
		}
	}
	if bridge.ID != "" && !bridgeUsed(g, bridge.ID) {
		if err := g.RemoveBridge(bridge.ID); err != nil {
			return err
		}
	}
	return destroyErr
}

// removable is why ep, or the bare bridge, may not go: it is the active
// endpoint, which is the connection the user falls back to, or a Bridge some
// Endpoint still reaches through.
func removable(g *config.Global, bridge config.Bridge, ep config.Endpoint) error {
	if ep != nil && ep == g.ActiveEndpoint() {
		return errors.New("connect to another endpoint before removing the active one")
	}
	if ep == nil && bridge.ID != "" {
		for _, other := range g.Settings.Endpoints {
			if through(other, bridge.ID) {
				return fmt.Errorf("bridge %s is used by endpoint %s; remove that connection instead", bridge.Name, other.URL())
			}
		}
	}
	return nil
}

func bridgeUsed(g *config.Global, bridgeID string) bool {
	for _, ep := range g.Settings.Endpoints {
		if through(ep, bridgeID) {
			return true
		}
	}
	return false
}

// through reports whether ep is reached through bridgeID.
func through(ep config.Endpoint, bridgeID string) bool {
	bridged, ok := ep.(config.BridgeEndpoint)
	return ok && bridged.BridgeID() == bridgeID
}
