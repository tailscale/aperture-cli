package bridges

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
	"tailscale.com/ipn/ipnstate"
)

type pendingNode struct {
	*fakeNode
	started, cancelling, releaseUp, closing, releaseClose chan struct{}
}

func (n *pendingNode) Up(ctx context.Context) (*ipnstate.Status, error) {
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
	m := NewManager(false)
	calls := 0
	m.newNode = func(config.Bridge, string, func(string, ...any), func(string, ...any)) tailnetNode {
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
		_, err := m.Activate(ctx, bridge, "http://ai", nil)
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
		url, err := m.Activate(ctx, bridge, "http://100.64.0.2", nil)
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
	url, err := m.Activate(context.Background(), bridge, "http://100.64.0.2", nil)
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

func (n *needsLoginNode) Up(ctx context.Context) (*ipnstate.Status, error) {
	n.up++
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSwitchTailnetDoesNotRequireAuthorization(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	n := &needsLoginNode{fakeNode: &fakeNode{}}
	m := NewManager(false)
	m.newNode = func(config.Bridge, string, func(string, ...any), func(string, ...any)) tailnetNode { return n }
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := m.SwitchTailnet(ctx, config.Bridge{ID: "bridge-abcdef", Name: "Work"}, nil)
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
	m := NewManager(false)
	m.newNode = func(config.Bridge, string, func(string, ...any), func(string, ...any)) tailnetNode { return n }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge := config.Bridge{ID: "bridge-abcdef", Name: "Work"}
	activationDone := make(chan error, 1)
	go func() {
		_, err := m.Activate(ctx, bridge, "http://ai", nil)
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
	if _, err := m.Activate(context.Background(), bridge, "http://ai", nil); !errors.Is(err, net.ErrClosed) {
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
			m := NewManager(false)
			m.newNode = func(config.Bridge, string, func(string, ...any), func(string, ...any)) tailnetNode { return node }
			if _, err := m.Activate(context.Background(), config.Bridge{ID: "bridge-abcdef"}, "http://100.64.0.2", nil); err != nil {
				t.Fatal(err)
			}
			release := sync.OnceFunc(func() { close(node.release) })
			defer release()
			results := make(chan error, 2)
			go func() { results <- m.Close() }()
			<-node.closing
			if _, err := m.Activate(context.Background(), config.Bridge{ID: "bridge-abcdef"}, "http://ai", nil); !errors.Is(err, net.ErrClosed) {
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
	for _, m := range []*Manager{nil, new(Manager), NewManager(false)} {
		if err := m.Close(); err != nil {
			t.Errorf("closing an unused manager: %v", err)
		}
	}
}
