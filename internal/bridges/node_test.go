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
