package profiles

import (
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

// RegisterIfSupported registers the Claude Desktop client on platforms that
// support it (darwin, windows). Call from the cmd entrypoint after importing
// the other client packages, to keep platform gating in one place.
func RegisterIfSupported() {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return
	}
	clients.Register(&desktopClient{})
}

// desktopClient adapts ClaudeDesktopProfile to the clients.Client contract.
// Unlike the CLI clients, Claude Desktop has no provider step: it always
// routes to Anthropic via the aperture gateway. The adapter's Menu is a
// single-item "launch" action; Install writes platform gateway config and
// returns a download command.
type desktopClient struct{}

const (
	desktopName      = "Claude Cowork"
	desktopBackendID = string(BackendAnthropic)
)

func (c *desktopClient) Name() string          { return desktopName }
func (c *desktopClient) BinaryName() string    { return platformBinaryName() }
func (c *desktopClient) CommonPaths() []string { return platformCommonPaths() }

func (c *desktopClient) IsInstalled() bool {
	return clients.IsInstalled(c.BinaryName(), c.CommonPaths())
}

func (c *desktopClient) Install(g *config.Global) clients.InstallPlan {
	return clients.InstallPlan{
		Hint: platformInstallHint(),
		Run: func() (*exec.Cmd, error) {
			if err := platformConfigure(GatewayURL(g.ApertureHost)); err != nil {
				return nil, err
			}
			return platformInstallCmd(), nil
		},
	}
}

func (c *desktopClient) Uninstall() clients.UninstallPlan {
	// Desktop uninstall is user-driven via the OS — no scripted path today.
	return clients.UninstallPlan{
		Hint: "Uninstall Claude from your operating system's app manager.",
	}
}

func (c *desktopClient) Menu(g *config.Global) menu.MenuItem {
	return menu.MenuItem{
		Label:  desktopName,
		Action: func() menu.Result { return c.launch(g) },
	}
}

func (c *desktopClient) launch(g *config.Global) menu.Result {
	_ = g.RecordLaunch(config.LaunchState{
		LastClientName:  desktopName,
		LastBackendType: desktopBackendID,
	})
	host := g.ApertureHost
	cmd := func() tea.Msg {
		wantURL := GatewayURL(host)
		if currentURL := platformReadGatewayURL(); currentURL != wantURL {
			if err := platformConfigure(wantURL); err != nil {
				return menu.LaunchDoneMsg{Err: err}
			}
		}
		return menu.LaunchDoneMsg{Err: platformLaunch()}
	}
	return menu.Result{Cmd: cmd, PopOnDone: true}
}

func (c *desktopClient) Replay(g *config.Global) tea.Cmd {
	if g.LastLaunch.LastClientName != desktopName || !c.IsInstalled() {
		return nil
	}
	res := c.launch(g)
	return res.Cmd
}

func (c *desktopClient) QuickSelectLabel(g *config.Global) string {
	return desktopName + " (Anthropic via Claude Cowork)"
}
