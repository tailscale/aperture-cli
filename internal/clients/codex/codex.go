// Package codex is the OpenAI Codex client. It speaks OpenAI's /v1/responses
// API and is registered only with providers whose compatibility map includes
// "openai_responses". On launch it writes a CODEX_HOME containing auth.json
// (pre-populated so the first run skips interactive login) and config.toml
// (pointing Codex at the aperture gateway).
package codex

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

// Client is the OpenAI Codex client.
type Client struct{}

const (
	name       = "OpenAI Codex"
	binaryName = "codex"
	compatKey  = "openai_responses"
)

// Name implements clients.Client.
func (c *Client) Name() string { return name }

// BinaryName implements clients.Client.
func (c *Client) BinaryName() string { return binaryName }

// CommonPaths implements clients.Client.
func (c *Client) CommonPaths() []string {
	return commonBinaryPaths()
}

// IsInstalled implements clients.Client.
func (c *Client) IsInstalled() bool {
	return clients.IsInstalled(binaryName, c.CommonPaths())
}

// Install implements clients.Client.
func (c *Client) Install(_ *config.Global) clients.InstallPlan {
	return clients.InstallPlan{
		Hint: "npm install -g @openai/codex",
		Run: func() (*exec.Cmd, error) {
			return exec.Command("/bin/sh", "-c", "npm install -g @openai/codex"), nil
		},
	}
}

// Uninstall implements clients.Client.
func (c *Client) Uninstall() clients.UninstallPlan {
	return clients.UninstallPlan{
		Hint: "npm uninstall -g @openai/codex",
		Run: func() error {
			return exec.Command("npm", "uninstall", "-g", "@openai/codex").Run()
		},
	}
}

// Menu implements clients.Client. Codex speaks only OpenAI /v1/responses,
// so the flow is: pick a compatible provider → pick a model → launch.
func (c *Client) Menu(g *config.Global) menu.MenuItem {
	return menu.MenuItem{
		Label: name,
		Action: func() menu.Result {
			return c.providerStep(g)
		},
	}
}

// providerStep builds the provider menu, or descends directly if only one
// provider is compatible.
func (c *Client) providerStep(g *config.Global) menu.Result {
	provs := compatibleProviders(g.Providers)
	if len(provs) == 0 {
		return errorResult("No providers support OpenAI /v1/responses.")
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

// modelStep shows the model picker when the provider has multiple models,
// or descends straight to launch with the single model.
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

// launch writes CODEX_HOME, builds the exec spec, records the launch state,
// and returns a tea.Cmd.
func (c *Client) launch(g *config.Global, p config.ProviderInfo, model string) menu.Result {
	bin := clients.FindBinary(binaryName, c.CommonPaths())
	if bin == "" {
		bin = binaryName
	}
	codexHome, err := writeConfig(g.ApertureHost)
	if err != nil {
		return errorResult("Failed to write Codex config: " + err.Error())
	}
	env := map[string]string{
		"OPENAI_BASE_URL": g.ApertureHost + "/v1",
		"OPENAI_API_KEY":  "not-needed",
		"CODEX_HOME":      codexHome,
	}
	if model != "" {
		env["OPENAI_MODEL"] = stripProviderPrefix(model)
	}

	args := []string{}
	if model != "" {
		args = append(args, "--model", model)
	}
	if g.Settings.YoloMode {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}

	_ = g.RecordLaunch(config.LaunchState{
		LastClientName:  name,
		LastBackendType: "openai",
		LastProviderID:  p.ID,
		LastModel:       model,
	})

	cmd := clients.Launch(clients.LaunchSpec{
		Binary: bin,
		Args:   args,
		Env:    env,
		Debug:  g.Debug,
	})
	return menu.Result{Cmd: cmd, PopOnDone: true}
}

// Replay implements clients.Client.
func (c *Client) Replay(g *config.Global) tea.Cmd {
	if g.LastLaunch.LastClientName != name {
		return nil
	}
	if !c.IsInstalled() {
		return nil
	}
	prov, ok := g.Provider(g.LastLaunch.LastProviderID)
	if !ok {
		return nil
	}
	if !prov.Compatibility[compatKey] {
		return nil
	}
	model := g.LastLaunch.LastModel
	if model != "" && !slices.Contains(fqnModels(prov), model) {
		return nil
	}
	// Launch. The Cmd inside Result is a tea.Cmd, so unwrap.
	res := c.launch(g, prov, model)
	return res.Cmd
}

// QuickSelectLabel implements clients.Client.
func (c *Client) QuickSelectLabel(g *config.Global) string {
	prov, _ := g.Provider(g.LastLaunch.LastProviderID)
	label := name + " via " + prov.DisplayName() + " - OpenAI Compatible"
	if g.LastLaunch.LastModel != "" {
		label += " - " + g.LastLaunch.LastModel
	}
	return label
}

// compatibleProviders returns the subset of providers that Codex can use.
func compatibleProviders(all []config.ProviderInfo) []config.ProviderInfo {
	var out []config.ProviderInfo
	for _, p := range all {
		if p.Compatibility[compatKey] {
			out = append(out, p)
		}
	}
	return out
}

// fqnModels returns the provider's models in "provider_id/model_id" form.
func fqnModels(p config.ProviderInfo) []string {
	out := make([]string, len(p.Models))
	for i, m := range p.Models {
		out[i] = p.ID + "/" + m
	}
	return out
}

func stripProviderPrefix(fqn string) string {
	if _, after, ok := strings.Cut(fqn, "/"); ok {
		return after
	}
	return fqn
}

// errorResult returns a Result that pops the current stack and emits an
// error via the TUI's generic error mechanism. The TUI interprets a Cmd
// that returns an error-bearing SimpleDoneMsg as "show this error".
func errorResult(msg string) menu.Result {
	return menu.Result{Cmd: func() tea.Msg {
		return menu.SimpleDoneMsg{Err: errString(msg)}
	}, PopOnDone: false}
}

type errString string

func (e errString) Error() string { return string(e) }
