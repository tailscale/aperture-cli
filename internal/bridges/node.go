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

// BringUp waits for the node to be usable and reports each wait from the one
// IPN bus watch (ADR 0001, decision 4). tsnet.Server.Up runs a watch of its
// own, and a second consumer of the same bus is evicted when it lags. The
// eviction surfaces as a terminal "IPN bus consumer fell behind" on a login
// the user did nothing wrong in.
//
// Owning the wait means owning what Up did with it: a terminal ErrMessage
// and the check that a Running node has an address. resetServeStateOnce is
// left out because nothing here sets a serve config.
func (n *tsnetNode) BringUp(ctx context.Context, ev events) (*ipnstate.Status, error) {
	// LocalClient calls Start, so this is where the node begins registering.
	lc, err := n.server.LocalClient()
	if err != nil {
		return nil, err
	}
	// InitialHealthState is requested too. Health changes reach every watcher
	// regardless of mask, but a login that broke before this watch started
	// shows up only in the initial state. That is the reused node case.
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

// ipnBusWatch is the part of an IPN bus watch that bringUp reads. Tests run the
// loop against a recorded bus through it.
type ipnBusWatch interface {
	Next() (ipn.Notify, error)
}

// bringUp waits for Running on one watch and reports each phase as the node
// enters it. The login link comes off the bus rather than tsnet's five second
// poll loop. The poll loop hides a link that lands just after a tick: one
// bridge was killed a few hundred milliseconds before its link would have
// printed.
func bringUp(ctx context.Context, w ipnBusWatch, statusOf func(context.Context) (*ipnstate.Status, error), ev events) (*ipnstate.Status, error) {
	progress := bringUpProgress{ev: ev}
	for {
		notify, err := w.Next()
		if err != nil {
			return nil, err
		}
		if notify.ErrMessage != nil {
			return nil, fmt.Errorf("bridge backend: %s", *notify.ErrMessage)
		}
		progress.notify(&notify)
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

// bringUpProgress translates IPN bus notifications into connection phases. It
// remembers the last phase because the bus repeats states.
//
// ipn.NeedsLogin covers two waits that look identical and are not. Before a
// BrowseToURL arrives, the control plane has not answered and there is
// nothing to do. After it arrives, everything waits on the user. Reporting
// the backend state made a 29 second registration indistinguishable from
// someone who wandered off.
type bringUpProgress struct {
	ev    events
	phase connection.Phase
	// loginBroken records whether the login-state warning is up. Health state
	// is re-sent on every retry with a fresh request ID in the text, so
	// reporting on the text would add a line a second for as long as the
	// failure lasts.
	loginBroken bool
}

func (r *bringUpProgress) enter(p connection.Phase) {
	// A re-notified NeedsLogin after the link is already on screen would walk
	// the attempt backwards through a wait the user has already left.
	if p <= r.phase {
		return
	}
	r.phase = p
	r.ev.enter(p)
}

func (r *bringUpProgress) notify(n *ipn.Notify) {
	if n == nil {
		return
	}
	if n.State != nil {
		// Log the raw state, not just the phase. NoState and NeedsLogin are
		// one phase on screen on purpose, and telling them apart is the whole
		// question in a log. NoState means control has not answered the
		// register yet.
		slog.Info("bridge ipn state", "state", n.State.String())
		switch *n.State {
		case ipn.NoState, ipn.NeedsLogin:
			// Both map here, and NoState is the one that matters. A bridge
			// that never logged in sits in NoState for the whole of POST
			// /machine/register, so NoState is the wait, not a not-started-yet.
			// Tailscale's own comment reads "UIs should print Loading..."
			// (ipnlocal/local.go).
			r.enter(connection.AwaitingLoginLink)
		case ipn.NeedsMachineAuth:
			// NeedsMachineAuth has no phase of its own. We have never seen it,
			// and inventing a wait we cannot observe is worse than a line that
			// says what to go and do. Promote it if this turns out to be
			// common.
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
			// Log the rejection reason, never the link itself.
			slog.Error("unusable login link from the control plane", "err", err)
			// This does not fail the login. tsnet keeps printing its own copy
			// and the user can still finish by hand. The note tells them the
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
// answered with a 502 leaves the node in NeedsLogin with no BrowseToURL, so
// the attempt sits on "Waiting for a login link" while tsnet retries behind a
// backoff. The error is not a vizerror, so it never reaches Notify.ErrMessage.
//
// Only the login-state warnable is reported. The other warnables describe a
// node that is up and imperfect, and would bury the one line this attempt
// cares about.
func (r *bringUpProgress) health(state *health.State) {
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

// newTSNetNode returns the node factory production Machines use. Each node is
// a tsnet.Server on the bridge's state directory, named the way the admin
// console will show it.
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
