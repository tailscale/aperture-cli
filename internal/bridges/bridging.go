package bridges

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
)

const (
	providerFetchTimeout       = 10 * time.Second
	bridgeProviderFetchTimeout = 30 * time.Second
)

// destroyTimeout bounds the logout a removal waits on. /machine/register was
// hanging past 90 seconds on 2026-09-17 and logout is a round trip to the same
// place, so a removal cannot wait on it indefinitely (ADR 0002, decision 6).
var destroyTimeout = 45 * time.Second

// Bridging is the Connection context's domain service. It connects the user
// to an Aperture from an Endpoint, through a Bridge's Machine when the
// Endpoint names one, and keeps the Bridge record and its Machine in
// agreement: joining records the tailnet on the Bridge, switching clears it,
// removing the Bridge destroys the Machine first (ADR 0002). It holds no state
// of its own.
//
// Every operation that waits on the network is split in two. The waiting half
// (Run, Destroy) takes a context and may run on any goroutine. The half that
// reads or writes settings (Begin, Commit, Abandon, Forget) must run where
// settings are read, which for the TUI is its update loop: nothing else
// serializes access to config.Global.
type Bridging struct {
	Machines *Machines
	Settings *config.Global
}

// Attempt is one try at reaching an Aperture from one Endpoint. It remembers
// what it wrote to settings on the user's behalf, so that abandoning it can
// take that back out, and which Endpoint it is an edit of, so that success can
// commit the edit and the removal of the original in one write (ADR 0003).
type Attempt struct {
	Endpoint config.Endpoint
	// InvalidatesActive reports that starting this attempt leaves the active
	// destination unverified: the Machine it launches through is being logged
	// out, and cancellation cannot prove the logout did not run (ADR 0003).
	InvalidatesActive bool

	bridge config.Bridge
	// ephemeral: Begin wrote Endpoint into settings so the failure screen has
	// something to name, retry and edit. Abandon removes it; failure keeps it.
	ephemeral     bool
	replaces      *config.Endpoint
	switchTailnet bool
}

// Bridge is the Bridge this attempt connects through, zero for a direct
// Endpoint.
func (a *Attempt) Bridge() config.Bridge { return a.bridge }

// SwitchesTailnet reports whether the attempt logs its Bridge out before
// connecting.
func (a *Attempt) SwitchesTailnet() bool { return a.switchTailnet }

// Ephemeral reports whether this attempt wrote its Endpoint into settings.
func (a *Attempt) Ephemeral() bool { return a.ephemeral }

// Retry is the same attempt again. A tailnet switch is not repeated: it ran,
// or failed, the first time, and the retry is about reaching the Endpoint.
func (a *Attempt) Retry() *Attempt {
	next := *a
	next.switchTailnet = false
	next.InvalidatesActive = false
	return &next
}

// Verified is what a successful attempt produced: the Gateway a client sends
// requests to, the providers it answered with and, through a Bridge, the
// tailnet the Machine joined.
type Verified struct {
	Gateway   string
	Tailnet   string
	Providers []config.ProviderInfo
}

// Begin prepares an attempt at ep. An Endpoint not yet in settings is written
// there first, so the failure screen has something to name, retry and edit;
// the attempt remembers it did that. replacing is the original of a URL edit,
// kept until the edit verifies. switchTailnet logs the Bridge out on the way
// and clears the tailnet recorded on it now: an abandoned login would
// otherwise leave the picker naming a tailnet the bridge has already left.
func (b Bridging) Begin(ep config.Endpoint, switchTailnet bool, replacing *config.Endpoint) (*Attempt, error) {
	a := &Attempt{Endpoint: ep, replaces: replacing}
	if ep.BridgeID != "" {
		bridge, ok := b.Settings.Bridge(ep.BridgeID)
		if !ok {
			return nil, fmt.Errorf("bridge %s is not configured", ep.BridgeID)
		}
		a.bridge = bridge
		if switchTailnet {
			if err := b.Settings.SetBridgeTailnet(ep.BridgeID, ""); err != nil {
				return nil, err
			}
			a.switchTailnet = true
			a.InvalidatesActive = ep.BridgeID == b.Settings.ActiveEndpoint().BridgeID
		}
	}
	if !b.configured(ep) {
		if err := b.Settings.UpsertEndpoint(ep); err != nil {
			return nil, err
		}
		a.ephemeral = true
	}
	return a, nil
}

// Retarget swaps the Endpoint an attempt probes for one the user typed,
// keeping the original of a pending edit. A candidate this attempt added is
// replaced rather than left behind: it was never reachable and nobody asked
// for it. The same Endpoint again is a retry.
func (b Bridging) Retarget(a *Attempt, next config.Endpoint) (*Attempt, error) {
	if config.SameEndpoint(next, a.Endpoint) {
		return a.Retry(), nil
	}
	ephemeral := !b.configured(next)
	switch {
	case a.ephemeral:
		if err := b.Settings.ReplaceEndpoint(a.Endpoint, next); err != nil {
			return nil, err
		}
	case ephemeral:
		if err := b.Settings.UpsertEndpoint(next); err != nil {
			return nil, err
		}
	}
	n := &Attempt{Endpoint: next, replaces: a.replaces, ephemeral: ephemeral}
	if next.BridgeID != "" {
		bridge, ok := b.Settings.Bridge(next.BridgeID)
		if !ok {
			return nil, fmt.Errorf("bridge %s is not configured", next.BridgeID)
		}
		n.bridge = bridge
	}
	return n, nil
}

// Edit verifies next before removing ep, keeping ep until it does (ADR 0003).
// When current is already an edit of ep, the new URL retargets it and the
// original stays the original; otherwise a new attempt replaces ep.
func (b Bridging) Edit(current *Attempt, ep, next config.Endpoint) (*Attempt, error) {
	if current != nil && config.SameEndpoint(current.Endpoint, ep) && current.replaces != nil {
		return b.Retarget(current, next)
	}
	return b.Begin(next, false, &ep)
}

// Run carries the attempt to a verified Gateway or an error, reporting each
// wait on emit. It writes nothing: Commit does, once the caller knows the
// result is still wanted.
func (b Bridging) Run(ctx context.Context, a *Attempt, emit func(connection.Event)) (Verified, error) {
	if a.Endpoint.BridgeID == "" {
		provs, err := fetchProviders(ctx, a.Endpoint.URL, providerFetchTimeout)
		if err != nil {
			return Verified{}, err
		}
		return Verified{Gateway: a.Endpoint.URL, Providers: provs}, nil
	}
	// Stamps the moment the user committed. Without it the first bridge line
	// is the earliest thing in the log and the gap in front of it reads as
	// startup cost rather than someone reading the menu.
	slog.Info("activating endpoint", "url", a.Endpoint.URL, "bridge", a.bridge.ID, "switchTailnet", a.switchTailnet)
	mc, err := b.Machines.For(a.bridge)
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
	route, err := mc.RouteTo(ctx, a.Endpoint.URL, emit)
	if err != nil {
		return Verified{}, err
	}
	// The longest silent stretch of the attempt: the bridge is up, so tsnet
	// has stopped logging and nothing else names the host being waited on.
	sink(emit).enter(connection.AskingForModels)
	provs, err := fetchProviders(ctx, route.LocalURL, bridgeProviderFetchTimeout)
	if err != nil {
		return Verified{}, fmt.Errorf("bridge %s could not reach %s: %w", a.bridge.Name, a.Endpoint.URL, err)
	}
	return Verified{Gateway: route.LocalURL, Tailnet: mc.Tailnet(), Providers: provs}, nil
}

// Commit makes a verified attempt the active connection. The Endpoint moves to
// the front of settings and a pending edit's original goes in the same write
// (ADR 0003); the tailnet joined is recorded on the Bridge so the picker can
// name it before the Machine exists again; the Gateway and providers become
// what clients launch against.
func (b Bridging) Commit(a *Attempt, v Verified) error {
	g := b.Settings
	if !config.SameEndpoint(g.ActiveEndpoint(), a.Endpoint) || a.replaces != nil {
		if err := g.SetActiveEndpoint(a.Endpoint, a.replaces); err != nil {
			return fmt.Errorf("could not save active endpoint: %w", err)
		}
	}
	a.replaces = nil
	a.ephemeral = false
	if a.Endpoint.BridgeID != "" && v.Tailnet != "" {
		// A failed write is not worth interrupting a connection that worked.
		if err := g.SetBridgeTailnet(a.Endpoint.BridgeID, v.Tailnet); err != nil {
			slog.Warn("could not record the bridge's tailnet", "bridge", a.Endpoint.BridgeID, "err", err)
		}
	}
	g.ApertureHost = v.Gateway
	g.Providers = v.Providers
	return nil
}

// Fail is the attempt not verifying. The candidate stays: the failure screen
// names it for retry and edit (ADR 0003). Reports whether the active
// destination is unverified as a result, which it is when the failing
// Endpoint is the active one.
func (b Bridging) Fail(a *Attempt) (invalidatesActive bool) {
	return config.SameEndpoint(a.Endpoint, b.Settings.ActiveEndpoint())
}

// Abandon is the user giving up on the attempt. The candidate it added comes
// back out of settings, so nothing the user did not choose is left behind.
func (b Bridging) Abandon(a *Attempt) error {
	if a == nil || !a.ephemeral {
		return nil
	}
	a.ephemeral = false
	return b.Settings.DropEndpoint(a.Endpoint)
}

// Tailnet is the network a Bridge reaches, preferring what its running
// Machine reports to what was saved: a bridge that switched tailnets this
// session leaves a stale name on disk until the next verified connection
// rewrites it.
func (b Bridging) Tailnet(bridge config.Bridge) string {
	if mc := b.Machines.lookup(bridge.ID); mc != nil {
		if name := mc.Tailnet(); name != "" {
			return name
		}
	}
	return bridge.Tailnet
}

// Removal is what one delete is about: the Bridge, and the Endpoint that was
// the last reason to keep it. Either can be absent.
type Removal struct {
	Bridge   config.Bridge
	Endpoint *config.Endpoint
}

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

// Destroys reports whether removing rem takes a Machine off a tailnet: rem is
// the Bridge's last Endpoint and the Bridge has started a Machine. A Bridge
// that never started has no device, and must not start one to find out. An
// error means rem may not go at all.
func (b Bridging) Destroys(rem Removal) (bool, error) {
	if err := b.removable(rem); err != nil {
		return false, err
	}
	if rem.Bridge.ID == "" || !HasMachine(rem.Bridge.ID) {
		return false, nil
	}
	for _, ep := range b.Settings.Settings.Endpoints {
		if ep.BridgeID != rem.Bridge.ID {
			continue
		}
		if rem.Endpoint == nil || !config.SameEndpoint(ep, *rem.Endpoint) {
			return false, nil
		}
	}
	return true, nil
}

// Destroy takes rem's Machine off its tailnet, waiting at most destroyTimeout
// for the tailnet to confirm. Settings are untouched: Forget drops them once
// the caller has the outcome, because they are the only record that the
// device exists. Only for a removal Destroys said yes to.
func (b Bridging) Destroy(ctx context.Context, rem Removal, emit func(connection.Event)) error {
	mc, err := b.Machines.For(rem.Bridge)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, destroyTimeout)
	defer cancel()
	err = mc.Destroy(ctx, emit)
	if err != nil && ctx.Err() != nil {
		return &Unconfirmed{Bridge: rem.Bridge, Wait: destroyTimeout, Err: err}
	}
	return err
}

// Forget drops the records rem covers, endpoint first: a Bridge an Endpoint
// still points at cannot be removed. destroyErr is Destroy's outcome, nil for
// a removal with nothing to destroy. A refusal keeps everything and is
// returned as is: the device is still on the tailnet and settings are the
// only thing naming it. A wait that expired drops the records and returns the
// *Unconfirmed, because the device may have outlived the wait.
func (b Bridging) Forget(rem Removal, destroyErr error) error {
	var unconfirmed *Unconfirmed
	if destroyErr != nil && !errors.As(destroyErr, &unconfirmed) {
		return destroyErr
	}
	if err := b.removable(rem); err != nil {
		return err
	}
	g := b.Settings
	if rem.Endpoint != nil {
		if err := g.DropEndpoint(*rem.Endpoint); err != nil {
			return err
		}
	}
	// Settings hold two objects where the picker shows one row, so removing
	// the endpoint alone left the bridge re-listed as a bare "Connect via"
	// row: to the user the row moved instead of going. A bridge two endpoints
	// reach through stays.
	if rem.Bridge.ID != "" && !b.bridgeUsed(rem.Bridge.ID) {
		if err := g.RemoveBridge(rem.Bridge.ID); err != nil {
			return err
		}
	}
	return destroyErr
}

// removable is why rem may not go: it is the active endpoint, which is the
// connection the user falls back to, or a bare Bridge some Endpoint still
// reaches through.
func (b Bridging) removable(rem Removal) error {
	if rem.Endpoint != nil && config.SameEndpoint(*rem.Endpoint, b.Settings.ActiveEndpoint()) {
		return errors.New("connect to another endpoint before removing the active one")
	}
	if rem.Endpoint == nil && rem.Bridge.ID != "" {
		for _, ep := range b.Settings.Settings.Endpoints {
			if ep.BridgeID == rem.Bridge.ID {
				return fmt.Errorf("bridge %s is used by endpoint %s; remove that connection instead", rem.Bridge.Name, ep.URL)
			}
		}
	}
	return nil
}

func (b Bridging) bridgeUsed(bridgeID string) bool {
	for _, ep := range b.Settings.Settings.Endpoints {
		if ep.BridgeID == bridgeID {
			return true
		}
	}
	return false
}

func (b Bridging) configured(want config.Endpoint) bool {
	for _, ep := range b.Settings.Settings.Endpoints {
		if config.SameEndpoint(ep, want) {
			return true
		}
	}
	return false
}

// fetchProviders asks an Aperture what it serves. This is the attempt's
// AskingForModels phase and the verification everything else waits on.
func fetchProviders(ctx context.Context, host string, timeout time.Duration) ([]config.ProviderInfo, error) {
	client := &http.Client{Timeout: timeout}
	url := strings.TrimRight(host, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Aperture intentionally filters model results for Claude Code user agents.
	// Discovery needs the full grant-filtered model list for every harness.
	req.Header.Set("User-Agent", "aperture-cli")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return nil, fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, url, detail)
		}
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	provs, err := config.ParseProviders(body)
	if err != nil {
		return nil, fmt.Errorf("could not parse models response: %w", err)
	}
	return provs, nil
}
