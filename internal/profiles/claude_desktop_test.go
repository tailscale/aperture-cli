package profiles

import (
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

func TestGatewayURL(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"http://ai", "https://ai"},
		{"https://my-aperture.ts.net", "https://my-aperture.ts.net"},
		{"http://ai/", "https://ai"},
		{"https://aperture.example.com/", "https://aperture.example.com"},
		{"ai.example.com", "https://ai.example.com"},
		{"http://ai:8080/", "https://ai:8080"},
	}
	for _, tt := range tests {
		if got := GatewayURL(tt.input); got != tt.want {
			t.Errorf("GatewayURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDesktopInstallSkipsImmediateBinaryCheck(t *testing.T) {
	plan := (&desktopClient{}).Install(&config.Global{})
	if !plan.SkipInstalledCheck {
		t.Error("Claude Cowork install must wait for the user instead of checking for the binary immediately")
	}
}
