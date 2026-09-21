package bridges

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
	"tailscale.com/health"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

type tailnetNode interface {
	BringUp(context.Context, events) (*ipnstate.Status, error)
	Status(context.Context) (*ipnstate.Status, error)
	DialContext(context.Context, string, string) (net.Conn, error)
	Logout(context.Context) error
	Close() error
}

type tsnetNode struct {
	server *tsnet.Server
}

// BringUp waits for the node to be usable and reports what it is waiting on,
// off the one IPN bus watch (ADR 0001, decision 4). tsnet.Server.Up runs a
// watch of its own, and a second consumer of the same bus is evicted when it
// lags, which arrives as a terminal "IPN bus consumer fell behind" on a login
// the user did nothing wrong in.
//
// Taking the wait means taking what Up did with it: a terminal ErrMessage, and
// the check that a Running node actually has an address. resetServeStateOnce
// is not ours to keep; nothing here sets a serve config.
func (n *tsnetNode) BringUp(ctx context.Context, ev events) (*ipnstate.Status, error) {
	// LocalClient calls Start, so this is where the node begins registering.
	lc, err := n.server.LocalClient()
	if err != nil {
		return nil, err
	}
	// InitialHealthState too: health changes reach every watcher regardless of
	// mask, but a login already broken before this watch started shows up only
	// in the initial one, which is the reused node case.
	watcher, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyInitialHealthState)
	if err != nil {
		return nil, err
	}
	defer watcher.Close()
	return bringUp(ctx, watcher, lc.Status, ev)
}

func (n *tsnetNode) Status(ctx context.Context) (*ipnstate.Status, error) {
	lc, err := n.server.LocalClient()
	if err != nil {
		return nil, err
	}
	return lc.Status(ctx)
}

func (n *tsnetNode) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return n.server.Dial(ctx, network, address)
}

// notifier is the part of an IPN bus watch the bring-up reads, so the loop can
// be exercised against a recorded bus.
type notifier interface {
	Next() (ipn.Notify, error)
}

// bringUp waits for Running on one watch, naming each wait as it is entered.
// The link comes off the bus rather than tsnet's five second poll loop, which
// hides a link that lands just after a tick: one bridge was killed a few
// hundred milliseconds before its link would have printed.
func bringUp(ctx context.Context, w notifier, statusOf func(context.Context) (*ipnstate.Status, error), ev events) (*ipnstate.Status, error) {
	reporter := loginReporter{ev: ev}
	for {
		notify, err := w.Next()
		if err != nil {
			return nil, err
		}
		if notify.ErrMessage != nil {
			return nil, fmt.Errorf("bridge backend: %s", *notify.ErrMessage)
		}
		reporter.notify(&notify)
		if notify.State == nil || *notify.State != ipn.Running {
			continue
		}
		status, err := statusOf(ctx)
		if err != nil {
			return nil, err
		}
		if status == nil || len(status.TailscaleIPs) == 0 {
			return nil, errors.New("bridge node is running with no tailnet address")
		}
		return status, nil
	}
}

// loginReporter turns IPN bus notifications into the phases a connection
// attempt reports, holding the last one because the bus repeats states.
//
// ipn.NeedsLogin covers two waits that look identical and are not: before a
// BrowseToURL the control plane has not answered and there is nothing to do,
// after it everything is waiting on the user. Reporting the backend state made
// a 29 second registration indistinguishable from someone who wandered off.
type loginReporter struct {
	ev    events
	phase connection.Phase
	// loginBroken is whether the login-state warning is up. Health state is
	// re-sent on every retry with a fresh request ID in the text, so reporting
	// on the text would add a line a second for as long as the failure lasts.
	loginBroken bool
}

func (r *loginReporter) enter(p connection.Phase) {
	// A re-notified NeedsLogin after the link is already on screen would walk
	// the attempt backwards through a wait the user has already left.
	if p <= r.phase {
		return
	}
	r.phase = p
	r.ev.enter(p)
}

func (r *loginReporter) notify(n *ipn.Notify) {
	if n == nil {
		return
	}
	if n.State != nil {
		// The raw state, not just the phase: NoState and NeedsLogin are one
		// phase on screen on purpose and the whole question in a log. NoState
		// means control has not answered the register yet.
		slog.Info("bridge ipn state", "state", n.State.String())
		switch *n.State {
		case ipn.NoState, ipn.NeedsLogin:
			// Both, and NoState is the one that matters: a bridge that never
			// logged in sits there for the whole of POST /machine/register, so
			// it is the wait and not a not-started-yet. Tailscale's own comment
			// reads "UIs should print Loading..." (ipnlocal/local.go).
			r.enter(connection.AwaitingLoginLink)
		case ipn.NeedsMachineAuth:
			// No phase of its own: we have never seen it, and inventing a wait
			// we cannot observe is worse than a line that says what to go and
			// do. Promote it if this turns out to be common.
			r.ev.note("This bridge is waiting to be approved in the tailnet's admin console.")
		case ipn.Starting:
			r.enter(connection.JoiningTailnet)
		case ipn.Running:
			r.enter(connection.FindingEndpoint)
		}
	}
	if n.BrowseToURL != nil {
		link, err := connection.ParseLoginLink(*n.BrowseToURL)
		if err != nil {
			// Record the rejection reason, never the authorization capability.
			slog.Error("unusable login link from the control plane", "err", err)
			// Not fatal to the login: tsnet keeps printing its own copy, and
			// the user can still finish by hand. Worth saying, because the
			// browser is not going to open.
			r.ev.note("Ignoring an unusable login link from the control plane: " + err.Error())
			return
		}
		r.enter(connection.AwaitingAuthorization)
		r.ev.loginRequired(link)
	}
	r.health(n.Health)
}

// health reports a login that is failing rather than merely slow. A register
// answered with a 502 leaves the node in NeedsLogin sending no BrowseToURL, so
// the attempt sits on "Waiting for a login link" while tsnet retries behind a
// backoff; the error is not a vizerror, so it never reaches Notify.ErrMessage.
//
// login-state only. The other warnables describe a node that is up and
// imperfect, and would bury the one line that is this attempt's business.
func (r *loginReporter) health(state *health.State) {
	if state == nil {
		return
	}
	warning, broken := state.Warnings[health.LoginStateWarnable.Code]
	if broken == r.loginBroken {
		return
	}
	r.loginBroken = broken
	if !broken {
		slog.Info("bridge login recovered")
		return
	}
	slog.Error("bridge login is failing", "text", redactDiagnostic(warning.Text))
	r.ev.note("The tailnet will not log this bridge in: " + warning.Text)
}

// Logout initializes the LocalAPI, but does not wait for authorization. A
// bridge whose old identity cannot log in must still be able to leave it.
func (n *tsnetNode) Logout(ctx context.Context) error {
	lc, err := n.server.LocalClient()
	if err != nil {
		return err
	}
	return lc.Logout(ctx)
}

func (n *tsnetNode) Close() error {
	return n.server.Close()
}

// newTSNetNode is how a Machine gets its node in production: a tsnet.Server on
// the bridge's state directory, named the way the admin console will show it.
func newTSNetNode(debug bool) func(bridge config.Bridge, stateDir string, userLogf, debugLogf func(string, ...any)) tailnetNode {
	return func(bridge config.Bridge, stateDir string, userLogf, debugLogf func(string, ...any)) tailnetNode {
		s := &tsnet.Server{
			Dir:      stateDir,
			Hostname: MachineName(bridge.ID),
			UserLogf: userLogf,
		}
		if debug {
			s.Logf = debugLogf
		}
		return &tsnetNode{server: s}
	}
}
