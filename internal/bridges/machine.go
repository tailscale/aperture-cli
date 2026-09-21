package bridges

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
)

// Machine is the device this program runs on the user's tailnet for one
// Bridge. It registers, may need a login, gets an address, carries dials and
// shows up under Machines in the admin console. A Machine outlives any one
// connection attempt and owns its Routes.
//
// A Machine runs one operation at a time. Open, RouteTo, LeaveTailnet,
// Destroy and Close each hold the Machine for their whole duration, cleanup
// included, so a second node can never open the state directory while a
// first one is still closing. An operation waiting its turn can be cancelled
// through its context without disturbing the running one. Close cancels the
// running one.
type Machine struct {
	bridge config.Bridge
	// machines is the collection this Machine belongs to. The collection holds the
	// node factory and dial tuning shared by every member.
	machines *Machines

	// turn is held by the operation running on this Machine. It is a one-slot
	// channel rather than a mutex so that waiting for it can be cancelled.
	turn chan struct{}
	// mu guards the fields another goroutine reads or sets while an operation
	// holds the turn: cancel, closed and tailnet.
	mu      sync.Mutex
	cancel  context.CancelFunc
	closed  bool
	tailnet string

	// The operation holding the turn owns these. A Machine in the collection
	// may have no node, either idle after a logout or never started.
	node   tailnetNode
	routes map[string]*Route
	ev     *eventRelay

	// slot is the bridge slot this Machine claimed when it first started a
	// node, and release frees its lock. Both are zero while the Machine has
	// never run one, and again after Destroy discards the slot's identity.
	slot    int
	release func()
}

func newMachine(bridge config.Bridge, ms *Machines) *Machine {
	return &Machine{
		bridge:   bridge,
		machines: ms,
		turn:     make(chan struct{}, 1),
		routes:   make(map[string]*Route),
		ev:       &eventRelay{},
	}
}

// MachineName returns the hostname the bridge's node registers under for a
// slot, which is the device name the tailnet shows. Removal has to name the
// same thing the admin console does, or a user told to delete it by hand
// cannot find it. Slot 1 keeps the name existing devices registered under.
func MachineName(bridgeID string, slot int) string {
	name := "aperture-cli-" + bridgeID
	if slot > 1 {
		name += "-" + strconv.Itoa(slot)
	}
	return name
}

// MachineNames returns the device names of every slot the bridge has on
// disk, in slot order, for screens that tell the user what a removal logs
// out.
func MachineNames(bridgeID string) ([]string, error) {
	slots, err := config.BridgeStateSlots(bridgeID)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(slots))
	for i, slot := range slots {
		names[i] = MachineName(bridgeID, slot)
	}
	return names, nil
}

// HasMachine reports whether the bridge ever started a node. tsnet creates
// the state directory on first use, so a missing directory is the only
// durable evidence that nothing was ever registered. Bridge.Tailnet cannot
// serve: it is a display hint, saved after verification and cleared before a
// switch.
func HasMachine(bridgeID string) bool {
	slots, err := config.BridgeStateSlots(bridgeID)
	return err == nil && len(slots) > 0
}

// begin takes the Machine for one operation and returns the context the
// operation runs under. Close can cancel that context.
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

// Tailnet returns the name of the tailnet the Machine joined. It is empty
// before the Machine joins one and after it leaves.
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
// emit. A Machine that is already open returns at once and points its
// reporting at emit. A failed start closes the node before returning, so the
// next Open starts clean rather than reusing a node another attempt was
// tearing down.
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

	// BringUp blocks until the node is Running. For a bridge that has never
	// logged in, that means blocking until the user visits a link nothing has
	// shown them yet. BringUp reports the wait from the watch it waits on.
	//
	// The wait is timed because every "it just sat there" report is about
	// this wait, and the number tells a slow control plane apart from a login
	// link the user never saw.
	start := time.Now()
	status, err := mc.node.BringUp(ctx, ev)
	if err != nil {
		slog.Error("bridge node did not come up", "bridge", mc.bridge.ID, "after", time.Since(start), "err", redactDiagnostic(err.Error()))
		return errors.Join(err, mc.shutdownNode())
	}
	slog.Info("bridge node up", "bridge", mc.bridge.ID, "after", time.Since(start))

	// The status returned by BringUp already names the tailnet, so no extra
	// call is needed. The connection picker shows the name on rows not
	// connected to yet.
	if status != nil && status.CurrentTailnet != nil && status.CurrentTailnet.Name != "" {
		mc.setTailnet(status.CurrentTailnet.Name)
	}
	return nil
}

// RouteTo returns the Route to remoteURL through this Machine, opening one on
// first use. The Machine must be open. RouteTo never starts a node to satisfy
// a Route.
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

	// Reported here so a reused Machine says it too. Otherwise a reused
	// Machine says nothing while the first dial waits for the target to
	// appear in its peer map.
	ev.enter(connection.FindingEndpoint)
	if mc.machines.debug {
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

// LeaveTailnet logs the Machine out of its tailnet and closes its node, so
// the next Open asks for a login. Logout needs only an initialized LocalAPI.
// Waiting for Running first would demand authorization of an expired or
// unapproved identity just to leave it.
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

// Destroy logs the Machine out of its tailnet and deletes the state
// directory holding its login. Destroy touches no settings. The Bridge record
// is the only thing naming the device, so the caller removes it after
// Destroy returns nil and keeps it otherwise (ADR 0002). It covers the slot
// this Machine holds; Machines.Destroy covers the slots past processes left
// behind.
//
// A Machine that never started has no device and must not start one to find
// out. Bring-up would demand the interactive login that is being removed.
//
// The state directory goes last and only on success. It holds the node key,
// which a later attempt needs to deregister the device. The slot stays locked
// until the work finishes: a claimant while it runs would mint a fresh
// identity that the removal then deletes.
//
// Destroy returns when the work is done or ctx ends, whichever is first.
// Logout takes ctx but the node's Close does not, and a close that hangs must
// not hold the caller past its deadline. Within the process the Machine stays
// held until the work finishes; another process never opens the directory
// under a close still running because the slot lock outlasts the return.
func (mc *Machine) Destroy(ctx context.Context, emit func(connection.Event)) error {
	stateDir, err := config.BridgeStateDir(mc.bridge.ID, mc.slot)
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

// destroyHeld does Destroy's work. The caller holds the Machine. Whatever the
// outcome, the slot is released when the work finishes: until then the lock
// is the only thing keeping another process from opening the state directory
// under a close still running, and after it the identity is gone or the
// caller is retrying with a fresh claim. Destroy may already have returned at
// its deadline, so nothing outside this goroutine may touch the slot.
func (mc *Machine) destroyHeld(ctx context.Context, stateDir string, ev events) error {
	if mc.node == nil && mc.slot == 0 {
		mc.setTailnet("")
		return nil
	}
	defer mc.releaseSlot()
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

// Close ends the Machine for the process. It interrupts the running
// operation, waits for that operation to finish cleaning up, closes the node
// and every Route, frees the Machine's slot, and refuses further operations.
// Close is safe to call more than once.
func (mc *Machine) Close() error {
	mc.mu.Lock()
	mc.closed = true
	if mc.cancel != nil {
		mc.cancel()
	}
	mc.mu.Unlock()
	mc.turn <- struct{}{}
	defer func() { <-mc.turn }()
	err := mc.shutdownNode()
	mc.releaseSlot()
	return err
}

// releaseSlot frees the lock guarding the Machine's slot. The caller holds
// the turn.
func (mc *Machine) releaseSlot() {
	if mc.release != nil {
		mc.release()
		mc.release = nil
	}
	mc.slot = 0
}

// initNode constructs a node without waiting for login. The caller holds the
// turn. Only Open follows initNode with BringUp.
//
// Constructing a node is what claims the bridge's slot: the claim is a lock
// on the slot number, so a second aperture process opening the same bridge
// takes the next number and never the same node key.
func (mc *Machine) initNode(ev events) error {
	mc.ev.forwardTo(ev)
	if mc.node != nil {
		return nil
	}
	if mc.machines.newNode == nil {
		return fmt.Errorf("bridge node is not configured")
	}
	if mc.slot == 0 {
		slot, release, err := claimSlot(mc.bridge.ID)
		if err != nil {
			return err
		}
		mc.slot, mc.release = slot, release
	}
	stateDir, err := config.BridgeStateDir(mc.bridge.ID, mc.slot)
	if err != nil {
		return err
	}
	// Both of tsnet's loggers are diagnostics now. Everything the attempt
	// waits on comes off the IPN bus, and UserLogf is mostly printAuthURLLoop
	// reprinting a link the footer already shows. The logger is a no-op
	// rather than nil, because tsnet falls back to log.Printf, which writes
	// over the TUI.
	logNotes := func(format string, args ...any) {
		if mc.machines.debug {
			events(mc.ev.emit).notef(format, args...)
		}
	}
	mc.node = mc.machines.newNode(mc.bridge, mc.slot, stateDir, logNotes, logNotes)
	if mc.node == nil {
		return fmt.Errorf("bridge node is not configured")
	}
	return nil
}

// shutdownNode closes every Route and the node, leaving the Machine idle. The
// caller holds the turn. shutdownNode must finish before a new node can open
// the same state directory.
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
