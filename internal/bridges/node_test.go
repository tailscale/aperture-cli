package bridges

import (
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

func TestNewTSNetNodeReadsAuthKeyFromEnv(t *testing.T) {
	t.Setenv("TS_AUTHKEY", "tskey-auth-test")

	node := newTSNetNode(false)(config.Bridge{ID: "bridge-abcdef"}, 1, t.TempDir(), nil, nil)
	ts, ok := node.(*tsnetNode)
	if !ok {
		t.Fatalf("newTSNetNode returned %T, want *tsnetNode", node)
	}
	if ts.server.AuthKey != "tskey-auth-test" {
		t.Errorf("AuthKey = %q, want the TS_AUTHKEY value", ts.server.AuthKey)
	}
}

// The Tailnet ID has to reach tsnet for the registration to ask control for
// that tailnet; without it control offers every tailnet the user is in.
func TestNewTSNetNodeRegistersForTheBridgesTailnet(t *testing.T) {
	node := newTSNetNode(false)(config.Bridge{ID: "bridge-abcdef", TailnetID: "TWork1CNTRL"}, 1, t.TempDir(), nil, nil)
	ts, ok := node.(*tsnetNode)
	if !ok {
		t.Fatalf("newTSNetNode returned %T, want *tsnetNode", node)
	}
	if ts.server.Tailnet != "TWork1CNTRL" {
		t.Errorf("Tailnet = %q, want the bridge's tailnet ID", ts.server.Tailnet)
	}
}
