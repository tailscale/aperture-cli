package bridges

import (
	"context"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
)

// activateMachine is the bridged half of Bridging.Run without settings: open
// the bridge's Machine and route to remoteURL. Most tests here want exactly
// that shape.
func activateMachine(ms *Machines, ctx context.Context, bridge config.Bridge, remoteURL string, emit func(connection.Event)) (string, error) {
	mc, err := ms.For(bridge)
	if err != nil {
		return "", err
	}
	if err := mc.Open(ctx, emit); err != nil {
		return "", err
	}
	route, err := mc.RouteTo(ctx, remoteURL, emit)
	if err != nil {
		return "", err
	}
	return route.LocalURL, nil
}

func switchTailnet(ms *Machines, ctx context.Context, bridge config.Bridge, emit func(connection.Event)) error {
	mc, err := ms.For(bridge)
	if err != nil {
		return err
	}
	return mc.LeaveTailnet(ctx, emit)
}

func destroyMachine(ms *Machines, ctx context.Context, bridge config.Bridge, emit func(connection.Event)) error {
	mc, err := ms.For(bridge)
	if err != nil {
		return err
	}
	return mc.Destroy(ctx, emit)
}

func tailnetOf(ms *Machines, bridgeID string) string {
	mc := ms.lookup(bridgeID)
	if mc == nil {
		return ""
	}
	return mc.Tailnet()
}
