package bridges

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/connection"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

// notifies replays a recorded bus. The second return of Next is what a closed
// watch gives the caller, so a sequence that never reaches Running ends the
// loop rather than hanging it.
type notifies struct {
	seq  []*ipn.Notify
	next int
}

func (n *notifies) Next() (ipn.Notify, error) {
	if n.next >= len(n.seq) {
		return ipn.Notify{}, errors.New("bus closed")
	}
	notify := n.seq[n.next]
	n.next++
	return *notify, nil
}

func addressed(addr string) *ipnstate.Status {
	return &ipnstate.Status{TailscaleIPs: []netip.Addr{netip.MustParseAddr(addr)}}
}

// TestBringUpReportsFromTheWatchItWaitsOn is ADR 0001 decision 4: one watch
// both names the wait and decides when it is over. Two watchers on a backend
// that assumes one is how a lagging consumer is evicted mid-login and reported
// as an unrelated bring-up failure.
func TestBringUpReportsFromTheWatchItWaitsOn(t *testing.T) {
	const url = "https://login.tailscale.com/a/28ba393017981"
	var got []string
	want := addressed("100.64.0.2")

	status, err := bringUp(
		context.Background(),
		&notifies{seq: []*ipn.Notify{
			state(ipn.NoState),
			browse(url),
			state(ipn.Starting),
			state(ipn.Running),
		}},
		func(context.Context) (*ipnstate.Status, error) { return want, nil },
		collect(&got),
		"",
	)
	if err != nil || status != want {
		t.Fatalf("bringUp = %v, %v; want the running status", status, err)
	}
	reported := strings.Join(got, "\n")
	for _, line := range []string{
		connection.AwaitingLoginLink.String(),
		connection.LoginRequired(mustLink(t, url)).String(),
		connection.JoiningTailnet.String(),
	} {
		if !strings.Contains(reported, line) {
			t.Errorf("bring-up never reported %q:\n%s", line, reported)
		}
	}
}

// TestBringUpFailsOnABackendError keeps what tsnet.Up did with ErrMessage: it
// is terminal, and a bring-up that kept waiting on it would sit on its last
// phase for as long as the user let it.
func TestBringUpFailsOnABackendError(t *testing.T) {
	msg := "IPN bus consumer fell behind"
	_, err := bringUp(
		context.Background(),
		&notifies{seq: []*ipn.Notify{{ErrMessage: &msg}, state(ipn.Running)}},
		func(context.Context) (*ipnstate.Status, error) {
			t.Error("status fetched after a backend error")
			return nil, nil
		},
		sink(nil),
		"",
	)
	if err == nil || !strings.Contains(err.Error(), msg) {
		t.Fatalf("bringUp error = %v, want the backend message", err)
	}
}

// TestBringUpRefusesARunningNodeWithNoAddress is tsnet.Up's own check, and it
// has to survive the move: Running with no address dials nothing, and failing
// here names the node instead of the endpoint.
func TestBringUpRefusesARunningNodeWithNoAddress(t *testing.T) {
	_, err := bringUp(
		context.Background(),
		&notifies{seq: []*ipn.Notify{state(ipn.Running)}},
		func(context.Context) (*ipnstate.Status, error) { return &ipnstate.Status{}, nil },
		sink(nil),
		"",
	)
	if err == nil || !strings.Contains(err.Error(), "address") {
		t.Fatalf("bringUp error = %v, want a running node with no address refused", err)
	}
}

// TestBringUpHoldsAPinnedBridgeToItsTailnet is tsnet.Up's tailnet check,
// which owning the watch means repeating (ADR 0009). Control refuses a fresh
// login to another tailnet, but state saved before the pin comes up Running
// wherever it was, and a node let through there dials the wrong tailnet.
func TestBringUpHoldsAPinnedBridgeToItsTailnet(t *testing.T) {
	const pinned tailcfg.StableTailnetID = "TWork1CNTRL"
	in := func(id tailcfg.StableTailnetID) *ipnstate.Status {
		st := addressed("100.64.0.2")
		st.CurrentTailnet = &ipnstate.TailnetStatus{Name: "example.com", StableID: id}
		return st
	}
	tests := []struct {
		name   string
		want   tailcfg.StableTailnetID
		status *ipnstate.Status
		ok     bool
	}{
		{name: "pinned and in that tailnet", want: pinned, status: in(pinned), ok: true},
		{name: "pinned and in another tailnet", want: pinned, status: in("THome2CNTRL")},
		{name: "pinned and control reports no ID", want: pinned, status: in("")},
		{name: "pinned and no tailnet at all", want: pinned, status: addressed("100.64.0.2")},
		{name: "unpinned joins any tailnet", status: in("THome2CNTRL"), ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := bringUp(
				context.Background(),
				&notifies{seq: []*ipn.Notify{state(ipn.Running)}},
				func(context.Context) (*ipnstate.Status, error) { return tt.status, nil },
				sink(nil),
				tt.want,
			)
			if tt.ok {
				if err != nil {
					t.Fatalf("bringUp = %v, want the node up", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), string(pinned)) {
				t.Fatalf("bringUp error = %v, want the wrong tailnet refused naming %s", err, pinned)
			}
		})
	}
}
