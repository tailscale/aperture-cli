// Package copilot is the GitHub Copilot CLI client. It supports three backend
// flavors — OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages —
// and configures routing entirely via environment variables (no config files).
package copilot

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

// Client is the GitHub Copilot CLI client.
type Client struct{}

const (
	name       = "GitHub Copilot"
	binaryName = "copilot"
)

type backend struct {
	id           string
	displayName  string
	compatKey    string
	providerType string
	wireAPI      string
}

var backends = []backend{
	{id: "openai_chat", displayName: "OpenAI Chat Completions", compatKey: "openai_chat", providerType: "openai", wireAPI: "completions"},
	{id: "openai_responses", displayName: "OpenAI Responses", compatKey: "openai_responses", providerType: "openai", wireAPI: "responses"},
	{id: "anthropic", displayName: "Anthropic Messages", compatKey: "anthropic_messages", providerType: "anthropic"},
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
		Hint: "npm install -g @github/copilot",
		Run: func() (*exec.Cmd, error) {
			return exec.Command("/bin/sh", "-c", "npm install -g @github/copilot"), nil
		},
	}
}

// Uninstall implements clients.Client.
func (c *Client) Uninstall() clients.UninstallPlan {
	return clients.UninstallPlan{
		Hint: "npm uninstall -g @github/copilot",
		Run: func() error {
			return exec.Command("npm", "uninstall", "-g", "@github/copilot").Run()
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
		return errorResult("No providers support GitHub Copilot CLI.")
	}
	if len(provs) == 1 {
		return c.backendStep(g, provs[0])
	}
	items := make([]menu.MenuItem, 0, len(provs))
	for _, p := range provs {
		items = append(items, menu.MenuItem{
			Label:       p.DisplayName(),
			Description: p.Description,
			Action:      func() menu.Result { return c.backendStep(g, p) },
		})
	}
	return menu.Result{Next: &menu.Menu{
		Title: "Choose a provider for " + name + ":",
		Items: items,
	}}
}

func (c *Client) backendStep(g *config.Global, p config.ProviderInfo) menu.Result {
	bs := backendsFor(p)
	if len(bs) == 0 {
		return errorResult("No compatible backends for " + p.DisplayName() + ".")
	}
	if len(bs) == 1 {
		return c.modelStep(g, p, bs[0])
	}
	items := make([]menu.MenuItem, 0, len(bs))
	for _, b := range bs {
		items = append(items, menu.MenuItem{
			Label:  b.displayName,
			Action: func() menu.Result { return c.modelStep(g, p, b) },
		})
	}
	return menu.Result{Next: &menu.Menu{
		Title: "Choose a backend for " + name + " via " + p.DisplayName() + ":",
		Items: items,
	}}
}

func (c *Client) modelStep(g *config.Global, p config.ProviderInfo, b backend) menu.Result {
	models := fqnModels(p)
	if len(models) <= 1 {
		var m string
		if len(models) == 1 {
			m = models[0]
		}
		return c.launch(g, p, b, m)
	}
	items := make([]menu.MenuItem, 0, len(models))
	for _, m := range models {
		items = append(items, menu.MenuItem{
			Label:  m,
			Action: func() menu.Result { return c.launch(g, p, b, m) },
		})
	}
	return menu.Result{Next: &menu.Menu{
		Title: "Choose a default model for " + name + " via " + p.DisplayName() + ":",
		Items: items,
	}}
}

func (c *Client) launch(g *config.Global, p config.ProviderInfo, b backend, model string) menu.Result {
	bin := clients.FindBinary(binaryName, c.CommonPaths())
	if bin == "" {
		bin = binaryName
	}

	env := buildEnv(g.ApertureHost, b, model)

	_ = g.RecordLaunch(config.LaunchState{
		LastClientName:  name,
		LastBackendType: b.id,
		LastProviderID:  p.ID,
		LastModel:       model,
	})

	cmd := clients.Launch(clients.LaunchSpec{
		Binary: bin,
		Env:    env,
		Debug:  g.Debug,
	})
	return menu.Result{Cmd: cmd, PopOnDone: true}
}

func buildEnv(apertureHost string, b backend, model string) map[string]string {
	host := strings.TrimRight(apertureHost, "/")
	env := map[string]string{
		"COPILOT_PROVIDER_TYPE":    b.providerType,
		"COPILOT_PROVIDER_API_KEY": "not-needed",
		"COPILOT_OFFLINE":          "true",
	}
	if b.providerType == "openai" {
		env["COPILOT_PROVIDER_BASE_URL"] = host + "/v1"
	} else {
		env["COPILOT_PROVIDER_BASE_URL"] = host
	}
	if b.wireAPI != "" {
		env["COPILOT_PROVIDER_WIRE_API"] = b.wireAPI
	}
	if model != "" {
		env["COPILOT_MODEL"] = stripProviderPrefix(model)
	}
	return env
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
	idx := slices.IndexFunc(backends, func(b backend) bool {
		return b.id == g.LastLaunch.LastBackendType
	})
	if idx < 0 {
		return nil
	}
	b := backends[idx]
	if !prov.Compatibility[b.compatKey] {
		return nil
	}
	model := g.LastLaunch.LastModel
	if model != "" && !slices.Contains(fqnModels(prov), model) {
		return nil
	}
	res := c.launch(g, prov, b, model)
	return res.Cmd
}

// QuickSelectLabel implements clients.Client.
func (c *Client) QuickSelectLabel(g *config.Global) string {
	prov, _ := g.Provider(g.LastLaunch.LastProviderID)
	b := g.LastLaunch.LastBackendType
	for _, bb := range backends {
		if bb.id == b {
			b = bb.displayName
			break
		}
	}
	label := name + " via " + prov.DisplayName() + " - " + b
	if g.LastLaunch.LastModel != "" {
		label += " - " + g.LastLaunch.LastModel
	}
	return label
}

func compatibleProviders(all []config.ProviderInfo) []config.ProviderInfo {
	var out []config.ProviderInfo
	for _, p := range all {
		if len(backendsFor(p)) > 0 {
			out = append(out, p)
		}
	}
	return out
}

func backendsFor(p config.ProviderInfo) []backend {
	var out []backend
	for _, b := range backends {
		if p.Compatibility[b.compatKey] {
			out = append(out, b)
		}
	}
	return out
}

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

func errorResult(msg string) menu.Result {
	return menu.Result{Cmd: func() tea.Msg {
		return menu.SimpleDoneMsg{Err: errString(msg)}
	}}
}

type errString string

func (e errString) Error() string { return string(e) }
