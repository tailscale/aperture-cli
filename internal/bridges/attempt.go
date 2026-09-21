package bridges

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
)

// Attempt is one try at reaching an Aperture from one Endpoint: the
// ConnectionAttempt of the domain model. It remembers what it wrote to
// settings on the user's behalf, so that abandoning it can take that back
// out, and which Endpoint it is an edit of, so that committing can replace
// the original in the same write (ADR 0003).
//
// Run waits on the network and writes nothing, so it may run on any
// goroutine. Everything else reads or writes settings and runs where settings
// are read, which for the TUI is its update loop: nothing else serializes
// access to config.Global.
type Attempt struct {
	Endpoint config.Endpoint
	// InvalidatesActive reports that starting this attempt leaves the active
	// destination unverified: the Machine it launches through is being logged
	// out, and cancellation cannot prove the logout did not run (ADR 0003).
	InvalidatesActive bool
	// TargetsActive reports that this attempt is at the active Endpoint, so
	// its failure leaves the active destination unverified too.
	TargetsActive bool

	bridge config.Bridge
	// ephemeral: BeginAttempt wrote Endpoint into settings so the failure
	// screen has something to name, retry and edit. Abandon removes it;
	// failure keeps it.
	ephemeral     bool
	replaces      config.Endpoint
	switchTailnet bool
}

// BeginAttempt prepares an attempt at ep. An Endpoint not yet in settings is
// written there first, so the failure screen has something to name, retry and
// edit; the attempt remembers it did that. replacing is the original of a URL
// edit, kept until the edit verifies. switchTailnet logs the Bridge out on the
// way and clears the tailnet recorded on it now: an abandoned login would
// otherwise leave the picker naming a tailnet the bridge has already left.
func BeginAttempt(g *config.Global, ep config.Endpoint, switchTailnet bool, replacing config.Endpoint) (*Attempt, error) {
	if ep == nil {
		return nil, fmt.Errorf("no endpoint to connect to")
	}
	a := &Attempt{Endpoint: ep, replaces: replacing, TargetsActive: ep == g.ActiveEndpoint()}
	if bridged, ok := ep.(config.BridgeEndpoint); ok {
		bridge, found := g.Bridge(bridged.BridgeID())
		if !found {
			return nil, fmt.Errorf("bridge %s is not configured", bridged.BridgeID())
		}
		a.bridge = bridge
		if switchTailnet {
			if err := g.SetBridgeTailnet(bridge.ID, ""); err != nil {
				return nil, err
			}
			a.switchTailnet = true
			active, _ := g.ActiveEndpoint().(config.BridgeEndpoint)
			a.InvalidatesActive = active.BridgeID() == bridge.ID
		}
	}
	if !slices.Contains(g.Settings.Endpoints, ep) {
		if err := g.UpsertEndpoint(ep); err != nil {
			return nil, err
		}
		a.ephemeral = true
	}
	return a, nil
}

// EditAttempt verifies next before removing ep, keeping ep until it does
// (ADR 0003). When current is already an edit of ep, the new URL retargets it
// and the original stays the original; otherwise a new attempt replaces ep.
func EditAttempt(g *config.Global, current *Attempt, ep, next config.Endpoint) (*Attempt, error) {
	if current != nil && current.Endpoint == ep && current.replaces != nil {
		return current.Retarget(g, next)
	}
	return BeginAttempt(g, next, false, ep)
}

// Retarget swaps the Endpoint this attempt probes for one the user typed,
// keeping the original of a pending edit. A candidate this attempt added is
// replaced rather than left behind: it was never reachable and nobody asked
// for it. The same Endpoint again is a retry.
func (a *Attempt) Retarget(g *config.Global, next config.Endpoint) (*Attempt, error) {
	if next == a.Endpoint {
		return a.Retry(), nil
	}
	ephemeral := !slices.Contains(g.Settings.Endpoints, next)
	switch {
	case a.ephemeral:
		if err := g.ReplaceEndpoint(a.Endpoint, next); err != nil {
			return nil, err
		}
	case ephemeral:
		if err := g.UpsertEndpoint(next); err != nil {
			return nil, err
		}
	}
	n := &Attempt{Endpoint: next, replaces: a.replaces, ephemeral: ephemeral, TargetsActive: next == g.ActiveEndpoint()}
	if bridged, ok := next.(config.BridgeEndpoint); ok {
		bridge, found := g.Bridge(bridged.BridgeID())
		if !found {
			return nil, fmt.Errorf("bridge %s is not configured", bridged.BridgeID())
		}
		n.bridge = bridge
	}
	return n, nil
}

// Retry is the same attempt again. A tailnet switch is not repeated: it ran,
// or failed, the first time, and the retry is about reaching the Endpoint.
func (a *Attempt) Retry() *Attempt {
	next := *a
	next.switchTailnet = false
	next.InvalidatesActive = false
	return &next
}

// Bridge is the Bridge this attempt connects through, zero for a direct
// Endpoint.
func (a *Attempt) Bridge() config.Bridge { return a.bridge }

// SwitchesTailnet reports whether the attempt logs its Bridge out before
// connecting.
func (a *Attempt) SwitchesTailnet() bool { return a.switchTailnet }

// Ephemeral reports whether this attempt wrote its Endpoint into settings.
func (a *Attempt) Ephemeral() bool { return a.ephemeral }

// Verified is what a successful attempt produced: the Gateway a client sends
// requests to, the providers it answered with and, through a Bridge, the
// tailnet the Machine joined.
type Verified struct {
	Gateway   string
	Tailnet   string
	Providers []config.ProviderInfo
}

// Run carries the attempt to a verified Gateway or an error, reporting each
// wait on emit. It writes nothing: Commit does, once the caller knows the
// result is still wanted.
func (a *Attempt) Run(ctx context.Context, machines *Machines, emit func(connection.Event)) (Verified, error) {
	bridged, ok := a.Endpoint.(config.BridgeEndpoint)
	if !ok {
		provs, err := fetchProviders(ctx, a.Endpoint.URL(), providerFetchTimeout)
		if err != nil {
			return Verified{}, err
		}
		return Verified{Gateway: a.Endpoint.URL(), Providers: provs}, nil
	}
	// Stamps the moment the user committed. Without it the first bridge line
	// is the earliest thing in the log and the gap in front of it reads as
	// startup cost rather than someone reading the menu.
	slog.Info("activating endpoint", "url", redactURL(bridged.URL()), "bridge", a.bridge.ID, "switchTailnet", a.switchTailnet)
	mc, err := machines.For(a.bridge)
	if err != nil {
		return Verified{}, err
	}
	// The switch shares the attempt's cancellation and event sink: the new
	// login link is what the user needs on screen, and Esc has to reach a
	// logout that stalls on the old tailnet.
	if a.switchTailnet {
		if err := mc.LeaveTailnet(ctx, emit); err != nil {
			return Verified{}, err
		}
	}
	if err := mc.Open(ctx, emit); err != nil {
		return Verified{}, err
	}
	route, err := mc.RouteTo(ctx, bridged.URL(), emit)
	if err != nil {
		return Verified{}, err
	}
	// The longest silent stretch of the attempt: the bridge is up, so tsnet
	// has stopped logging and nothing else names the host being waited on.
	sink(emit).enter(connection.AskingForModels)
	provs, err := fetchProviders(ctx, route.LocalURL, bridgeProviderFetchTimeout)
	if err != nil {
		return Verified{}, fmt.Errorf("bridge %s could not reach %s: %w", a.bridge.Name, bridged.URL(), err)
	}
	return Verified{Gateway: route.LocalURL, Tailnet: mc.Tailnet(), Providers: provs}, nil
}

// Commit makes a verified attempt the active connection. The Endpoint moves
// to the front of settings and a pending edit's original goes in the same
// write (ADR 0003); the tailnet joined is recorded on the Bridge so the picker
// can name it before the Machine exists again; the Gateway and providers
// become what clients launch against.
func (a *Attempt) Commit(g *config.Global, v Verified) error {
	if g.ActiveEndpoint() != a.Endpoint || a.replaces != nil {
		if err := g.SetActiveEndpoint(a.Endpoint, a.replaces); err != nil {
			return fmt.Errorf("could not save active endpoint: %w", err)
		}
	}
	a.replaces = nil
	a.ephemeral = false
	if a.bridge.ID != "" && v.Tailnet != "" {
		// A failed write is not worth interrupting a connection that worked.
		if err := g.SetBridgeTailnet(a.bridge.ID, v.Tailnet); err != nil {
			slog.Warn("could not record the bridge's tailnet", "bridge", a.bridge.ID, "err", err)
		}
	}
	g.ApertureHost = v.Gateway
	g.Providers = v.Providers
	return nil
}

// Abandon is the user giving up on the attempt. The candidate it added comes
// back out of settings, so nothing the user did not choose is left behind. A
// failed attempt is not abandoned: its candidate stays for retry and edit.
func (a *Attempt) Abandon(g *config.Global) error {
	if a == nil || !a.ephemeral {
		return nil
	}
	a.ephemeral = false
	return g.DropEndpoint(a.Endpoint)
}
