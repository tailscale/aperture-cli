// Package hermes is the Hermes Agent client. Hermes speaks OpenAI Chat
// Completions and accepts a custom endpoint through CUSTOM_BASE_URL, so this
// client configures routing entirely through environment variables and a
// provider argument without replacing the user's Hermes configuration.
package hermes

import (
	"os/exec"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

func init() {
	clients.Register(&Client{})
}

// Client is the Hermes Agent client.
type Client struct{}

const (
	name       = "Hermes Agent"
	binaryName = "hermes"
	compatKey  = "openai_chat"

	installCmd   = "curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash"
	uninstallCmd = "hermes uninstall --yes"
)

// Name implements clients.Client.
func (c *Client) Name() string { return name }

// BinaryName implements clients.Client.
func (c *Client) BinaryName() string { return binaryName }

// CommonPaths implements clients.Client.
func (c *Client) CommonPaths() []string { return commonBinaryPaths() }

// IsInstalled implements clients.Client.
func (c *Client) IsInstalled() bool { return clients.IsInstalled(binaryName, c.CommonPaths()) }

// Install implements clients.Client.
func (c *Client) Install(_ *config.Global) clients.InstallPlan {
	return clients.InstallPlan{
		Hint: installCmd,
		Run: func() (*exec.Cmd, error) {
			return exec.Command("/bin/sh", "-c", installCmd), nil
		},
	}
}

// Uninstall implements clients.Client.
func (c *Client) Uninstall() clients.UninstallPlan {
	return clients.UninstallPlan{
		Hint: uninstallCmd,
		Run: func() error {
			return exec.Command(binaryName, "uninstall", "--yes").Run()
		},
	}
}

// Menu implements clients.Client.
func (c *Client) Menu(g *config.Global) menu.MenuItem {
	return menu.MenuItem{Label: name, Action: func() menu.Result { return c.providerStep(g) }}
}

func (c *Client) providerStep(g *config.Global) menu.Result {
	provs := compatibleProviders(g.Providers)
	if len(provs) == 0 {
		return errorResult("No providers support " + name + ".")
	}
	if len(provs) == 1 {
		return c.modelStep(g, provs[0])
	}
	items := make([]menu.MenuItem, 0, len(provs))
	for _, p := range provs {
		items = append(items, menu.MenuItem{
			Label: p.DisplayName(), Description: p.Description,
			Action: func() menu.Result { return c.modelStep(g, p) },
		})
	}
	return menu.Result{Next: &menu.Menu{Title: "Choose a provider for " + name + ":", Items: items}}
}

func (c *Client) modelStep(g *config.Global, p config.ProviderInfo) menu.Result {
	models := fqnModels(p)
	if len(models) <= 1 {
		var model string
		if len(models) == 1 {
			model = models[0]
		}
		return c.launch(g, p, model)
	}
	items := make([]menu.MenuItem, 0, len(models))
	for _, model := range models {
		items = append(items, menu.MenuItem{Label: model, Action: func() menu.Result { return c.launch(g, p, model) }})
	}
	return menu.Result{Next: &menu.Menu{Title: "Choose a default model for " + name + " via " + p.DisplayName() + ":", Items: items}}
}

func (c *Client) launch(g *config.Global, p config.ProviderInfo, model string) menu.Result {
	bin := clients.FindBinary(binaryName, c.CommonPaths())
	if bin == "" {
		bin = binaryName
	}
	env := buildEnv(g.ApertureHost, model)
	args := buildArgs(g.Settings.YoloMode)
	_ = g.RecordLaunch(config.LaunchState{
		LastClientName: name, LastBackendType: compatKey, LastProviderID: p.ID, LastModel: model,
	})
	cmd := clients.Launch(clients.LaunchSpec{Binary: bin, Args: args, Env: env, Debug: g.Debug})
	return menu.Result{Cmd: cmd, PopOnDone: true}
}

func buildEnv(apertureHost, model string) map[string]string {
	env := map[string]string{
		"CUSTOM_BASE_URL": strings.TrimRight(apertureHost, "/") + "/v1",
	}
	if model != "" {
		env["HERMES_INFERENCE_MODEL"] = stripProviderPrefix(model)
	}
	return env
}

func buildArgs(yolo bool) []string {
	args := []string{"--provider", "custom"}
	if yolo {
		args = append(args, "--yolo")
	}
	return args
}

func resolveReplay(g *config.Global) (config.ProviderInfo, string, bool) {
	if g.LastLaunch.LastClientName != name || g.LastLaunch.LastBackendType != compatKey {
		return config.ProviderInfo{}, "", false
	}
	prov, ok := g.Provider(g.LastLaunch.LastProviderID)
	if !ok || !providerMatches(prov) {
		return config.ProviderInfo{}, "", false
	}
	model := g.LastLaunch.LastModel
	if model != "" && !slices.Contains(fqnModels(prov), model) {
		return config.ProviderInfo{}, "", false
	}
	return prov, model, true
}

// Replay implements clients.Client.
func (c *Client) Replay(g *config.Global) tea.Cmd {
	if !c.IsInstalled() {
		return nil
	}
	prov, model, ok := resolveReplay(g)
	if !ok {
		return nil
	}
	return c.launch(g, prov, model).Cmd
}

// QuickSelectLabel implements clients.Client.
func (c *Client) QuickSelectLabel(g *config.Global) string {
	prov, _ := g.Provider(g.LastLaunch.LastProviderID)
	label := name + " via " + prov.DisplayName()
	if g.LastLaunch.LastModel != "" {
		label += " - " + g.LastLaunch.LastModel
	}
	return label
}

func compatibleProviders(all []config.ProviderInfo) []config.ProviderInfo {
	var out []config.ProviderInfo
	for _, p := range all {
		if providerMatches(p) {
			out = append(out, p)
		}
	}
	return out
}

func providerMatches(p config.ProviderInfo) bool { return p.Compatibility[compatKey] }

func fqnModels(p config.ProviderInfo) []string {
	out := make([]string, len(p.Models))
	for i, model := range p.Models {
		out[i] = p.ID + "/" + model
	}
	return out
}

func stripProviderPrefix(fqn string) string {
	if _, after, ok := strings.Cut(fqn, "/"); ok {
		return after
	}
	return fqn
}

func errorResult(msg string) menu.Result {
	return menu.Result{Cmd: func() tea.Msg { return menu.SimpleDoneMsg{Err: errString(msg)} }}
}

type errString string

func (e errString) Error() string { return string(e) }
