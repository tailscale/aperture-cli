package tui

import "testing"

var statusName = map[tailscaleStatus]string{
	tsNotInstalled: "tsNotInstalled",
	tsNotRunning:   "tsNotRunning",
	tsNotConnected: "tsNotConnected",
	tsConnected:    "tsConnected",
}

func TestParseTailscaleStatus(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  tailscaleStatus
	}{
		{
			name:  "Running",
			input: []byte(`{"BackendState":"Running"}`),
			want:  tsConnected,
		},
		{
			name:  "NeedsLogin",
			input: []byte(`{"BackendState":"NeedsLogin"}`),
			want:  tsNotConnected,
		},
		{
			name:  "NeedsMachineAuth",
			input: []byte(`{"BackendState":"NeedsMachineAuth"}`),
			want:  tsNotConnected,
		},
		{
			name:  "Stopped",
			input: []byte(`{"BackendState":"Stopped"}`),
			want:  tsNotRunning,
		},
		{
			name:  "Empty",
			input: []byte(`{}`),
			want:  tsNotRunning,
		},
		{
			name:  "InvalidJSON",
			input: []byte(`not json`),
			want:  tsNotRunning,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTailscaleStatus(tc.input)
			if got != tc.want {
				t.Errorf("got %s (%d), want %s (%d)",
					statusName[got], got, statusName[tc.want], tc.want)
			}
		})
	}
}

func TestStatusName(t *testing.T) {
	// Verify the map covers all known constants.
	for _, s := range []tailscaleStatus{tsNotInstalled, tsNotRunning, tsNotConnected, tsConnected} {
		if _, ok := statusName[s]; !ok {
			t.Errorf("statusName missing entry for %d", s)
		}
	}
}

