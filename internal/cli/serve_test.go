package cli

import (
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

func TestServePort(t *testing.T) {
	stored := config.Bridge{Name: "work", Port: 8991}
	tests := []struct {
		name     string
		flagPort int
		bridge   config.Bridge
		want     int
		wantErr  bool
	}{
		{name: "flag wins over the stored port", flagPort: 8992, bridge: stored, want: 8992},
		{name: "stored port without the flag", bridge: stored, want: 8991},
		{name: "neither is an error", bridge: config.Bridge{Name: "work"}, wantErr: true},
		{name: "negative flag", flagPort: -1, bridge: stored, wantErr: true},
		{name: "too large flag", flagPort: 65536, bridge: stored, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := servePort(tt.flagPort, tt.bridge)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("servePort(%d, %+v) = %d, want an error", tt.flagPort, tt.bridge, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("servePort: %v", err)
			}
			if got != tt.want {
				t.Errorf("servePort(%d, %+v) = %d, want %d", tt.flagPort, tt.bridge, got, tt.want)
			}
		})
	}
}

func TestServeEndpoint(t *testing.T) {
	g := &config.Global{Settings: config.Settings{
		Endpoints: []config.EndpointEntry{
			{Endpoint: config.Direct("http://direct"), Name: "e1"},
			{Endpoint: config.Bridged("http://bridged", "bridge-abc123"), Name: "e2"},
			{Endpoint: config.Bridged("http://other-bridge", "bridge-fedcba"), Name: "e3"},
		},
	}}
	serve := config.Bridge{ID: "bridge-abc123", Name: "work"}
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "default is the well-known location", value: "", want: config.DefaultLocation},
		{name: "direct name", value: "e1", want: "http://direct"},
		{name: "name through this bridge", value: "e2", want: "http://bridged"},
		{name: "name through another bridge is a contradiction", value: "e3", wantErr: true},
		{name: "raw URL", value: "http://aperture.example.com", want: "http://aperture.example.com"},
		{name: "bare host", value: "ai", want: "http://ai"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := serveEndpoint(g, serve, tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("serveEndpoint(%q) = %q, want an error", tt.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("serveEndpoint(%q): %v", tt.value, err)
			}
			if got != tt.want {
				t.Errorf("serveEndpoint(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
