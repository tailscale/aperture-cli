package bridges

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
)

// Machine is what this program runs on the user's tailnet for one Bridge: it
// registers, may need a login, gets an address, carries dials and shows up
// under Machines in the admin console. It outlives any one connection attempt
// and is the aggregate root for its Routes.
//
// One operation at a time. Open, RouteTo, LeaveTailnet, Destroy and Close each
// hold the Machine for their whole duration, cleanup included, so a second
// node can never open the state directory a first one is still closing. An
// operation waiting its turn can be cancelled through its context without
// disturbing the one running; Close cancels the one running.
type Machine struct {
	bridge config.Bridge
	// of is the collection this Machine belongs to, which holds the node
	// factory and dial tuning shared by every member.
	of *Machines

	// turn is held by the operation running on this Machine. A one-slot channel
	// rather than a mutex so that waiting for it can be cancelled.
	turn chan struct{}
	// mu guards what another goroutine reads or sets while an operation holds
	// the turn: the running operation's cancel, closed and tailnet.
	mu      sync.Mutex
	cancel  context.CancelFunc
	closed  bool
	tailnet string

	// Owned by whoever holds the turn. A Machine in the collection may have no
	// node: idle after a logout, or never started.
	node   tailnetNode
	routes map[string]*Route
	ev     *eventRelay
}

func newMachine(bridge config.Bridge, ms *Machines) *Machine {
	return &Machine{
		bridge: bridge,
		of:     ms,
		turn:   make(chan struct{}, 1),
		routes: make(map[string]*Route),
		ev:     &eventRelay{},
	}
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

// begin takes the Machine for one operation and returns the context it runs
// under, which Close can cancel.
func (mc *Machine) begin(ctx context.Context) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	mc.mu.Lock()
	closed := mc.closed
	mc.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	select {
	case mc.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	if mc.closed {
		<-mc.turn
		return nil, net.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		<-mc.turn
		return nil, err
	}
	ctx, mc.cancel = context.WithCancel(ctx)
	return ctx, nil
}

func (mc *Machine) end() {
	mc.mu.Lock()
	if mc.cancel != nil {
		mc.cancel()
		mc.cancel = nil
	}
	mc.mu.Unlock()
	<-mc.turn
}

// Tailnet is the network the Machine joined, empty until it has or after it
// left.
func (mc *Machine) Tailnet() string {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return mc.tailnet
}

func (mc *Machine) setTailnet(name string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.tailnet = name
}

// Open brings the node up, logging in if it has to, and reports each wait on
// emit. An open Machine returns at once with its reporting pointed at emit. A
// failed start closes the node before returning, so the next Open starts
// clean rather than reusing a node another attempt was tearing down.
func (mc *Machine) Open(ctx context.Context, emit func(connection.Event)) error {
	ev := sink(emit)
	ctx, err := mc.begin(ctx)
	if err != nil {
		return err
	}
	defer mc.end()
	if mc.node != nil {
		mc.ev.forwardTo(ev)
		return nil
	}
	if err := mc.initNode(ev); err != nil {
		return err
	}

	ev.enter(connection.StartingMachine)

	// BringUp blocks until the node is Running, which for a bridge that has
	// never logged in means blocking until the user visits a link nothing has
	// shown them yet. It reports the wait off the watch it is waiting on.
	//
	// Timed because this is the wait every "it just sat there" report is
	// about, and the number is the difference between a slow control plane and
	// a login link the user never saw.
	start := time.Now()
	status, err := mc.node.BringUp(ctx, ev)
	if err != nil {
		slog.Error("bridge node did not come up", "bridge", mc.bridge.ID, "after", time.Since(start), "err", redactDiagnostic(err.Error()))
		return errors.Join(err, mc.shutdownNode())
	}
	slog.Info("bridge node up", "bridge", mc.bridge.ID, "after", time.Since(start))

	// The login status names the tailnet this bridge reaches at no extra
	// call. The connection picker shows it on rows not connected to yet.
	if status != nil && status.CurrentTailnet != nil && status.CurrentTailnet.Name != "" {
		mc.setTailnet(status.CurrentTailnet.Name)
	}
	return nil
}

// RouteTo opens, or returns, the Route to remoteURL through this Machine. The
// Machine must be open: a Route can only be created through an open Machine,
// and this never starts a node to satisfy one.
func (mc *Machine) RouteTo(ctx context.Context, remoteURL string, emit func(connection.Event)) (*Route, error) {
	target, err := parseTarget(remoteURL)
	if err != nil {
		return nil, err
	}
	ev := sink(emit)
	ctx, err = mc.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer mc.end()
	if mc.node == nil {
		return nil, fmt.Errorf("bridge %s is not open", mc.bridge.Name)
	}
	mc.ev.forwardTo(ev)

	// Reported here for a reused Machine too, which would otherwise say
	// nothing while the first dial waits for the target to appear in its
	// peer map.
	ev.enter(connection.FindingEndpoint)
	if mc.of.debug {
		// Full status rather than the login status: it lets debug output tell
		// a DNS problem from a target absent from this node's netmap, on
		// reuse too, since the endpoint may have changed.
		status, err := mc.node.Status(ctx)
		if err != nil {
			ev.note("Could not read bridge network status: " + err.Error())
		}
		logBridgeStatus(ev, status, target)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := target.String()
	if route := mc.routes[key]; route != nil {
		return route, nil
	}
	route, err := mc.openRoute(target)
	if err != nil {
		return nil, err
	}
	mc.routes[key] = route
	ev.note("Listening on " + route.LocalURL)
	return route, nil
}

// LeaveTailnet logs the Machine out of the tailnet it is on and closes its
// node, so the next Open asks for a login. Logout needs only an initialized
// LocalAPI: waiting for Running first would demand authorization of an
// expired or unapproved identity just to leave it.
func (mc *Machine) LeaveTailnet(ctx context.Context, emit func(connection.Event)) error {
	ev := sink(emit)
	ctx, err := mc.begin(ctx)
	if err != nil {
		return err
	}
	defer mc.end()
	if err := mc.initNode(ev); err != nil {
		return err
	}

	ev.note("Logging bridge " + mc.bridge.Name + " out of its tailnet ...")
	logoutErr := mc.node.Logout(ctx)
	closeErr := mc.shutdownNode()
	mc.setTailnet("")
	if err := errors.Join(logoutErr, closeErr); err != nil {
		return err
	}
	ev.note("Bridge logged out. Log in to the tailnet you want next.")
	return nil
}

// Destroy removes the Machine from its tailnet and discards the state
// directory holding its login. It touches no settings: the Bridge record is
// the only thing naming the device, so the caller drops it after this returns
// nil and keeps it otherwise (ADR 0002).
//
// A Machine that never started has no device and must not start one to find
// out: bring-up is what would demand the interactive login being removed.
//
// The state directory goes last and only on success: it holds the node key,
// which is what a later attempt would need to deregister the device.
//
// Returns when the work is done or ctx ends, whichever is first. Logout takes
// ctx but the node's Close does not, and a close that hangs must not hold the
// caller past its deadline. The Machine stays held until the work finishes,
// so the next operation waits rather than opening the state directory under a
// close still running.
func (mc *Machine) Destroy(ctx context.Context, emit func(connection.Event)) error {
	stateDir, err := config.BridgeStateDir(mc.bridge.ID)
	if err != nil {
		return err
	}
	ev := sink(emit)
	ctx, err = mc.begin(ctx)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		defer mc.end()
		done <- mc.destroyHeld(ctx, stateDir, ev)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// end cancels ctx right after the result is sent, so a result that
		// is already there wins over the cancellation it caused.
		select {
		case err := <-done:
			return err
		default:
			return ctx.Err()
		}
	}
}

// destroyHeld is Destroy's work, run with the Machine held.
func (mc *Machine) destroyHeld(ctx context.Context, stateDir string, ev events) error {
	if mc.node == nil && !HasMachine(mc.bridge.ID) {
		mc.setTailnet("")
		return nil
	}
	if err := mc.initNode(ev); err != nil {
		return err
	}

	ev.note("Removing bridge " + mc.bridge.Name + " from its tailnet ...")
	logoutErr := mc.node.Logout(ctx)
	closeErr := mc.shutdownNode()
	mc.setTailnet("")
	if err := errors.Join(logoutErr, closeErr); err != nil {
		return err
	}
	if err := os.RemoveAll(stateDir); err != nil {
		return err
	}
	ev.note("Bridge " + mc.bridge.Name + " is no longer a device on that tailnet.")
	return nil
}

// Close ends the Machine for the process: it interrupts the operation running,
// waits for it to finish cleaning up, closes the node and every Route, and
// refuses further operations. Safe to call more than once.
func (mc *Machine) Close() error {
	mc.mu.Lock()
	mc.closed = true
	if mc.cancel != nil {
		mc.cancel()
	}
	mc.mu.Unlock()
	mc.turn <- struct{}{}
	defer func() { <-mc.turn }()
	return mc.shutdownNode()
}

// initNode constructs a node without waiting for login. Called with the turn
// held; only Open follows it with BringUp.
func (mc *Machine) initNode(ev events) error {
	mc.ev.forwardTo(ev)
	if mc.node != nil {
		return nil
	}
	if mc.of.newNode == nil {
		return fmt.Errorf("bridge node is not configured")
	}
	stateDir, err := config.BridgeStateDir(mc.bridge.ID)
	if err != nil {
		return err
	}
	// Both of tsnet's loggers are diagnostics now: everything the attempt waits
	// on comes off the IPN bus, and UserLogf is mostly printAuthURLLoop
	// reprinting a link the footer already shows. A no-op rather than nil,
	// because tsnet falls back to log.Printf, which writes over the TUI.
	logNotes := func(format string, args ...any) {
		if mc.of.debug {
			events(mc.ev.emit).notef(format, args...)
		}
	}
	mc.node = mc.of.newNode(mc.bridge, stateDir, logNotes, logNotes)
	if mc.node == nil {
		return fmt.Errorf("bridge node is not configured")
	}
	return nil
}

// shutdownNode closes every Route and the node, leaving the Machine idle.
// Called with the turn held: it must finish before a new node can open the
// same state directory.
func (mc *Machine) shutdownNode() error {
	var errs []error
	for key, route := range mc.routes {
		errs = append(errs, route.close())
		delete(mc.routes, key)
	}
	if mc.node != nil {
		errs = append(errs, mc.node.Close())
		mc.node = nil
	}
	return errors.Join(errs...)
}
