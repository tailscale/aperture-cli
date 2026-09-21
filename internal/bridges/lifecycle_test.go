package bridges

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"tailscale.com/ipn/ipnstate"
)

type pendingNode struct {
	*fakeNode
	started, cancelling, releaseUp, closing, releaseClose chan struct{}
}

func (n *pendingNode) BringUp(ctx context.Context, _ events) (*ipnstate.Status, error) {
	close(n.started)
	<-ctx.Done()
	close(n.cancelling)
	<-n.releaseUp
	return nil, ctx.Err()
}

func (n *pendingNode) Close() error {
	close(n.closing)
	<-n.releaseClose
	return n.fakeNode.Close()
}

func TestActivateWaitsForCancelledNodeCleanup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	n := &pendingNode{
		fakeNode: &fakeNode{}, started: make(chan struct{}), cancelling: make(chan struct{}),
		releaseUp: make(chan struct{}), closing: make(chan struct{}), releaseClose: make(chan struct{}),
	}
	var upOnce, closeOnce sync.Once
	releaseUp := func() { upOnce.Do(func() { close(n.releaseUp) }) }
	releaseClose := func() { closeOnce.Do(func() { close(n.releaseClose) }) }
	defer releaseUp()
	defer releaseClose()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer backend.Close()
	replacement := &fakeNode{backendAddr: backend.Listener.Addr().String()}
	m := NewMachines(false)
	calls := 0
	m.newNode = func(config.Bridge, int, string, func(string, ...any), func(string, ...any)) tailnetNode {
		calls++
		if calls == 1 {
			return n
		}
		return replacement
	}
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := activateMachine(m, ctx, bridge, "http://ai", nil)
		first <- err
	}()
	<-n.started
	cancel()
	<-n.cancelling
	// A replacement must wait for both Up and Close, without holding the
	// manager's map lock or ignoring its own cancellation.
	checkWaiting := func(stage string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		url, err := activateMachine(m, ctx, bridge, "http://100.64.0.2", nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("during %s, replacement = %q, %v; want cancellable wait", stage, url, err)
		}
	}
	checkWaiting("Up cancellation")
	releaseUp()
	<-n.closing
	checkWaiting("Close")
	releaseClose()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("first activation: %v", err)
	}
	url, err := activateMachine(m, context.Background(), bridge, "http://100.64.0.2", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if replacement.up != 1 || calls != 2 {
		t.Errorf("new node not brought up once: calls=%d up=%d", calls, replacement.up)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("replacement proxy status=%d", resp.StatusCode)
	}
}

type needsLoginNode struct{ *fakeNode }

func (n *needsLoginNode) BringUp(ctx context.Context, _ events) (*ipnstate.Status, error) {
	n.up++
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSwitchTailnetDoesNotRequireAuthorization(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	n := &needsLoginNode{fakeNode: &fakeNode{}}
	m := NewMachines(false)
	m.newNode = func(config.Bridge, int, string, func(string, ...any), func(string, ...any)) tailnetNode { return n }
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := switchTailnet(m, ctx, config.Bridge{ID: "bridge-abcdef", Name: "Work"}, nil)
	if err != nil || n.loggedOut != 1 || n.up != 0 || !n.closed {
		t.Fatalf("switch = %v, up=%d logout=%d closed=%v; want logout without authorization", err, n.up, n.loggedOut, n.closed)
	}
}

func TestCloseCancelsStartupBeforeClosingNode(t *testing.T) {
	n := &pendingNode{
		fakeNode: &fakeNode{}, started: make(chan struct{}), cancelling: make(chan struct{}),
		releaseUp: make(chan struct{}), closing: make(chan struct{}), releaseClose: make(chan struct{}),
	}
	close(n.releaseUp)
	close(n.releaseClose)
	m := NewMachines(false)
	m.newNode = func(config.Bridge, int, string, func(string, ...any), func(string, ...any)) tailnetNode { return n }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work"}
	activationDone := make(chan error, 1)
	go func() {
		_, err := activateMachine(m, ctx, bridge, "http://ai", nil)
		activationDone <- err
	}()
	<-n.started
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the authorization wait")
	}
	if err := <-activationDone; !errors.Is(err, context.Canceled) {
		t.Errorf("activation = %v", err)
	}
	if !n.closed {
		t.Error("Close returned with a live node")
	}
	if _, err := activateMachine(m, context.Background(), bridge, "http://ai", nil); !errors.Is(err, net.ErrClosed) {
		t.Errorf("activation after shutdown = %v, want net.ErrClosed", err)
	}
}

type closingNode struct {
	*fakeNode
	closing, release chan struct{}
	err              error
}

func (n *closingNode) Close() error {
	close(n.closing)
	<-n.release
	_ = n.fakeNode.Close()
	return n.err
}

func TestConcurrentCloseSharesCompletionAndError(t *testing.T) {
	for _, closeErr := range []error{nil, errors.New("node shutdown failed")} {
		t.Run(fmt.Sprint(closeErr), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			node := &closingNode{fakeNode: &fakeNode{}, closing: make(chan struct{}), release: make(chan struct{}), err: closeErr}
			m := NewMachines(false)
			m.newNode = func(config.Bridge, int, string, func(string, ...any), func(string, ...any)) tailnetNode { return node }
			if _, err := activateMachine(m, context.Background(), config.Bridge{ID: "bridge-abcdef"}, "http://100.64.0.2", nil); err != nil {
				t.Fatal(err)
			}
			release := sync.OnceFunc(func() { close(node.release) })
			defer release()
			results := make(chan error, 2)
			go func() { results <- m.Close() }()
			<-node.closing
			if _, err := activateMachine(m, context.Background(), config.Bridge{ID: "bridge-abcdef"}, "http://ai", nil); !errors.Is(err, net.ErrClosed) {
				t.Errorf("activation during shutdown = %v", err)
			}
			go func() { results <- m.Close() }()
			var got []error
			select {
			case err := <-results:
				got = append(got, err)
				t.Errorf("Close returned %v before node cleanup finished", err)
			case <-time.After(30 * time.Millisecond):
			}
			release()
			for len(got) < 2 {
				got = append(got, <-results)
			}
			got = append(got, m.Close())
			for _, err := range got {
				if !errors.Is(err, closeErr) || err != got[0] {
					t.Errorf("Close results = %v; want one shared result wrapping %v", got, closeErr)
				}
			}
		})
	}
}

func TestCloseEmptyManager(t *testing.T) {
	for _, m := range []*Machines{nil, new(Machines), NewMachines(false)} {
		if err := m.Close(); err != nil {
			t.Errorf("closing an unused manager: %v", err)
		}
	}
}

// stateDir is the directory tsnet would have created for a bridge that has
// started once, holding the node key that names the registered device.
func stateDir(t *testing.T, bridgeID string) string {
	t.Helper()
	dir, err := config.BridgeStateDir(bridgeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tailscaled.state"), []byte("node key"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDestroyLeavesNoMachineBehind(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work"}
	dir := stateDir(t, bridge.ID)
	if !HasMachine(bridge.ID) {
		t.Fatal("a started bridge reports no machine")
	}
	n := &fakeNode{}
	m := NewMachines(false)
	m.newNode = func(config.Bridge, int, string, func(string, ...any), func(string, ...any)) tailnetNode { return n }
	defer m.Close()

	if err := destroyMachine(m, context.Background(), bridge, nil); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if n.loggedOut != 1 || !n.closed || n.up != 0 {
		t.Errorf("logout=%d closed=%v up=%d; want a logout and close without authorization", n.loggedOut, n.closed, n.up)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("state directory survived the machine: %v", err)
	}
	if HasMachine(bridge.ID) {
		t.Error("destroyed bridge still reports a machine")
	}
}

// A failed logout keeps the local records: they are the only thing naming the
// device, so the caller must be able to leave settings alone and say so.
func TestDestroyKeepsStateWhenTheTailnetRefuses(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work"}
	dir := stateDir(t, bridge.ID)
	n := &fakeNode{logoutErr: errors.New("control plane said no")}
	m := NewMachines(false)
	m.newNode = func(config.Bridge, int, string, func(string, ...any), func(string, ...any)) tailnetNode { return n }
	defer m.Close()

	if err := destroyMachine(m, context.Background(), bridge, nil); err == nil || !strings.Contains(err.Error(), "control plane said no") {
		t.Fatalf("Destroy = %v, want the logout failure", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("state discarded after a failed logout: %v", err)
	}
}

// A bridge nobody finished a login for has no device to deregister, and
// starting a node to discover that would demand the login it never had.
func TestDestroySkipsABridgeThatNeverStarted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work"}
	if HasMachine(bridge.ID) {
		t.Fatal("an unstarted bridge reports a machine")
	}
	m := NewMachines(false)
	m.newNode = func(config.Bridge, int, string, func(string, ...any), func(string, ...any)) tailnetNode {
		t.Error("started a node to remove a bridge that never had one")
		return &fakeNode{}
	}
	defer m.Close()

	if err := destroyMachine(m, context.Background(), bridge, nil); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

// The removal's wait has to bound cleanup too. Logout takes the context, but
// a node.Close that hangs after it would hold the removal past its deadline
// and the TUI with it. The Machine stays held until cleanup finishes, so the
// next operation on it waits rather than opening the state directory under a
// close still running.
func TestDestroyReturnsAtTheDeadlineWhileCloseHangs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work"}
	stateDir(t, bridge.ID)
	n := &pendingNode{fakeNode: &fakeNode{}, closing: make(chan struct{}), releaseClose: make(chan struct{})}
	m := NewMachines(false)
	var nodes atomic.Int32
	m.newNode = func(config.Bridge, int, string, func(string, ...any), func(string, ...any)) tailnetNode {
		// The hanging node once; the reopen after it gets an ordinary one. The
		// reopen runs while the destroy's close is still hanging, so this count
		// is shared between goroutines.
		if nodes.Add(1) == 1 {
			return n
		}
		return &fakeNode{status: tailnetStatus("ai.example.ts.net.", "100.64.0.2")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- destroyMachine(m, ctx, bridge, nil) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Destroy = %v, want the deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Destroy did not return at its deadline while Close hung")
	}

	// The reopen does not wait for the stuck close: slot 1 is still locked by
	// the destroy's cleanup, so the Open claims the next slot rather than
	// opening a state directory under a close still running.
	opened := make(chan error, 1)
	go func() { _, err := activateMachine(m, context.Background(), bridge, "http://ai", nil); opened <- err }()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open waited on the stuck destroy instead of taking the next slot")
	}
	close(n.releaseClose)
	m.Close()
}
