package profiles

import (
	"os/exec"
	"strings"
)

// ClaudeDesktopProfile implements Profile for the Claude Cowork application
// (Claude Code GUI). During install, it writes platform-specific gateway
// configuration (macOS plist / Windows registry) and downloads the installer.
// On launch, it re-checks the configuration and starts the desktop app.
type ClaudeDesktopProfile struct{}

func (c *ClaudeDesktopProfile) Name() string          { return "Claude Cowork" }
func (c *ClaudeDesktopProfile) BinaryName() string    { return platformBinaryName() }
func (c *ClaudeDesktopProfile) CommonPaths() []string { return platformCommonPaths() }

func (c *ClaudeDesktopProfile) SupportedBackends() []Backend {
	return []Backend{
		{Type: BackendAnthropic, DisplayName: "Anthropic API"},
	}
}

func (c *ClaudeDesktopProfile) RequiredCompat(b Backend) []string {
	switch b.Type {
	case BackendAnthropic:
		return []string{"anthropic_messages"}
	default:
		return nil
	}
}

func (c *ClaudeDesktopProfile) InstallHint() string { return platformInstallHint() }

// RunInstall writes the gateway configuration and returns a command that
// downloads and runs the installer. The TUI executes this with terminal
// takeover so the user sees download progress.
func (c *ClaudeDesktopProfile) RunInstall(apertureHost string) (*exec.Cmd, error) {
	if err := platformConfigure(GatewayURL(apertureHost)); err != nil {
		return nil, err
	}
	return platformInstallCmd(), nil
}

// Launch checks whether the gateway configuration matches the current aperture
// host, updates it if needed, and starts the desktop app.
func (c *ClaudeDesktopProfile) Launch(apertureHost string) error {
	wantURL := GatewayURL(apertureHost)
	if currentURL := platformReadGatewayURL(); currentURL != wantURL {
		if err := platformConfigure(wantURL); err != nil {
			return err
		}
	}
	return platformLaunch()
}

// Env is not used for desktop app profiles but satisfies the Profile interface.
func (c *ClaudeDesktopProfile) Env(_ string, _ Backend) (map[string]string, error) {
	return nil, nil
}

// GatewayURL normalizes the aperture host for Claude Cowork's gateway config.
// Claude Cowork requires HTTPS and no trailing slash.
func GatewayURL(apertureHost string) string {
	u := strings.Replace(apertureHost, "http://", "https://", 1)
	if !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	return strings.TrimRight(u, "/")
}
