// Package opencode is the OpenCode client. Unlike the other clients,
// OpenCode has a single abstract routing flavor: the real protocol (OpenAI
// Responses, OpenAI Chat, Anthropic Messages, Bedrock, Vertex, Gemini) is
// decided at launch time from the chosen provider's compatibility map. The
// Menu flow therefore skips the backend step and goes straight from
// provider selection to model selection.
package opencode

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

func init() {
	clients.Register(&Client{})
}

// Client is the OpenCode client.
type Client struct{}

const (
	name       = "OpenCode"
	binaryName = "opencode"
)

// compatKeys is the set of provider-compatibility flags OpenCode can
// translate into a working config. A provider matches if any one is set.
var compatKeys = []string{
	"openai_responses",
	"anthropic_messages",
	"openai_chat",
	"google_generate_content",
	"google_raw_predict",
	"bedrock_model_invoke",
	"bedrock_converse",
	"gemini_generate_content",
}

// Name implements clients.Client.
func (c *Client) Name() string { return name }

// BinaryName implements clients.Client.
func (c *Client) BinaryName() string { return binaryName }

// CommonPaths implements clients.Client.
func (c *Client) CommonPaths() []string { return commonBinaryPaths() }

// IsInstalled implements clients.Client.
func (c *Client) IsInstalled() bool {
	return clients.IsInstalled(binaryName, c.CommonPaths())
}

// Install implements clients.Client.
func (c *Client) Install(_ *config.Global) clients.InstallPlan {
	return clients.InstallPlan{
		Hint: "curl -fsSL https://opencode.ai/install | bash",
		Run: func() (*exec.Cmd, error) {
			return exec.Command("/bin/sh", "-c", "curl -fsSL https://opencode.ai/install | bash"), nil
		},
	}
}

// Uninstall implements clients.Client.
func (c *Client) Uninstall() clients.UninstallPlan {
	return clients.UninstallPlan{
		Hint: "opencode uninstall --force\nrm -rf ~/.opencode/bin",
		Run: func() error {
			if err := exec.Command("opencode", "uninstall", "--force").Run(); err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			return os.RemoveAll(filepath.Join(home, ".opencode", "bin"))
		},
	}
}

// Menu implements clients.Client.
func (c *Client) Menu(g *config.Global) menu.MenuItem {
	return menu.MenuItem{
		Label:  name,
		Action: func() menu.Result { return c.providerStep(g) },
	}
}

func (c *Client) providerStep(g *config.Global) menu.Result {
	provs := compatibleProviders(g.Providers)
	if len(provs) == 0 {
		return errorResult("No providers support an OpenCode protocol.")
	}
	if len(provs) == 1 {
		return c.modelStep(g, provs[0])
	}
	items := make([]menu.MenuItem, 0, len(provs))
	for _, p := range provs {
		items = append(items, menu.MenuItem{
			Label:       p.DisplayName(),
			Description: p.Description,
			Action:      func() menu.Result { return c.modelStep(g, p) },
		})
	}
	return menu.Result{Next: &menu.Menu{
		Title: "Choose a provider for " + name + ":",
		Items: items,
	}}
}

func (c *Client) modelStep(g *config.Global, p config.ProviderInfo) menu.Result {
	models := fqnModels(p)
	if len(models) <= 1 {
		var m string
		if len(models) == 1 {
			m = models[0]
		}
		return c.launch(g, p, m)
	}
	items := make([]menu.MenuItem, 0, len(models))
	for _, m := range models {
		items = append(items, menu.MenuItem{
			Label:  m,
			Action: func() menu.Result { return c.launch(g, p, m) },
		})
	}
	return menu.Result{Next: &menu.Menu{
		Title: "Choose a default model for " + name + " via " + p.DisplayName() + ":",
		Items: items,
	}}
}

func (c *Client) launch(g *config.Global, p config.ProviderInfo, model string) menu.Result {
	bin := clients.FindBinary(binaryName, c.CommonPaths())
	if bin == "" {
		bin = binaryName
	}
	configPath, cleanup, err := writeProviderConfig(g.ApertureHost, p)
	if err != nil {
		return errorResult("Failed to write OpenCode config: " + err.Error())
	}

	env := map[string]string{
		"OPENCODE_CONFIG": configPath,
	}
	// Bedrock SDK requires at least placeholder AWS credentials and region.
	if p.Compatibility["bedrock_model_invoke"] || p.Compatibility["bedrock_converse"] {
		env["AWS_ACCESS_KEY_ID"] = "not-needed"
		env["AWS_SECRET_ACCESS_KEY"] = "not-needed"
		env["AWS_REGION"] = "us-east-1"
	}

	// OpenCode has no documented yolo flag today; keep Args empty. Model is
	// conveyed via the provider config written above, not a CLI arg.
	_ = g.RecordLaunch(config.LaunchState{
		LastClientName:  name,
		LastBackendType: "openai", // historical; OpenCode's abstract backend
		LastProviderID:  p.ID,
		LastModel:       model,
	})

	cmd := clients.Launch(clients.LaunchSpec{
		Binary:  bin,
		Env:     env,
		Cleanup: cleanup,
		Debug:   g.Debug,
	})
	return menu.Result{Cmd: cmd, PopOnDone: true}
}

// Replay implements clients.Client.
func (c *Client) Replay(g *config.Global) tea.Cmd {
	if g.LastLaunch.LastClientName != name || !c.IsInstalled() {
		return nil
	}
	prov, ok := g.Provider(g.LastLaunch.LastProviderID)
	if !ok {
		return nil
	}
	if !providerMatches(prov) {
		return nil
	}
	model := g.LastLaunch.LastModel
	if model != "" && !slices.Contains(fqnModels(prov), model) {
		return nil
	}
	res := c.launch(g, prov, model)
	return res.Cmd
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

func providerMatches(p config.ProviderInfo) bool {
	for _, k := range compatKeys {
		if p.Compatibility[k] {
			return true
		}
	}
	return false
}

func fqnModels(p config.ProviderInfo) []string {
	out := make([]string, len(p.Models))
	for i, m := range p.Models {
		out[i] = p.ID + "/" + m
	}
	return out
}

func errorResult(msg string) menu.Result {
	return menu.Result{Cmd: func() tea.Msg {
		return menu.SimpleDoneMsg{Err: errString(msg)}
	}}
}

type errString string

func (e errString) Error() string { return string(e) }
