package pilike_test

import (
	"slices"
	"testing"

	"github.com/tailscale/aperture-cli/internal/clients"
	_ "github.com/tailscale/aperture-cli/internal/clients/omp"
	_ "github.com/tailscale/aperture-cli/internal/clients/pi"
	"github.com/tailscale/aperture-cli/internal/config"
)

// TestRegisteredClientNamesAreStable pins the other half of the
// persisted-state contract. A client's display name is written to state.json
// as LaunchState.LastClientName and compared against on replay, so renaming
// one makes every recorded launch for it unreplayable.
//
// It lives in the external test package because the names belong to the thin
// client packages, which import pilike.
func TestRegisteredClientNamesAreStable(t *testing.T) {
	var got []string
	for _, c := range clients.All(&config.Global{}) {
		got = append(got, c.Name())
	}
	for _, want := range []string{"Pi", "Oh My Pi"} {
		if !slices.Contains(got, want) {
			t.Errorf("registered clients = %v, missing %q", got, want)
		}
	}
}
