// Package omp is the Oh My Pi client. OMP accepts custom providers through
// its pi.registerProvider extension API, so this client writes a per-launch
// extension without replacing the user's OMP settings, credentials, or sessions.
// The menu flow is provider, wire protocol, then model.
package omp

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

// Client is the Oh My Pi client.
type Client struct{}

const (
	name       = "Oh My Pi"
	binaryName = "omp"

	installCmd   = "bun install -g @oh-my-pi/pi-coding-agent"
	uninstallCmd = "bun uninstall -g @oh-my-pi/pi-coding-agent"
)

type backend struct {
	id          string
	displayName string
	api         string
	compatKeys  []string
}

var backends = []backend{
	{id: "openai_responses", displayName: "OpenAI Responses", api: "openai-responses", compatKeys: []string{"openai_responses"}},
	{id: "anthropic", displayName: "Anthropic Messages", api: "anthropic-messages", compatKeys: []string{"anthropic_messages"}},
	{id: "openai_chat", displayName: "OpenAI Chat Completions", api: "openai-completions", compatKeys: []string{"openai_chat"}},
	{id: "vertex", displayName: "Google Vertex", api: "google-generative-ai", compatKeys: []string{"google_generate_content", "google_raw_predict"}},
}

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
			return exec.Command("bun", "uninstall", "-g", "@oh-my-pi/pi-coding-agent").Run()
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
		return c.backendStep(g, provs[0])
	}
	items := make([]menu.MenuItem, 0, len(provs))
	for _, p := range provs {
		items = append(items, menu.MenuItem{
			Label: p.DisplayName(), Description: p.Description,
			Action: func() menu.Result { return c.backendStep(g, p) },
		})
	}
	return menu.Result{Next: &menu.Menu{Title: "Choose a provider for " + name + ":", Items: items}}
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
		items = append(items, menu.MenuItem{Label: b.displayName, Action: func() menu.Result { return c.modelStep(g, p, b) }})
	}
	return menu.Result{Next: &menu.Menu{Title: "Choose a backend for " + name + " via " + p.DisplayName() + ":", Items: items}}
}

func (c *Client) modelStep(g *config.Global, p config.ProviderInfo, b backend) menu.Result {
	models := fqnModels(p)
	if len(models) <= 1 {
		var model string
		if len(models) == 1 {
			model = models[0]
		}
		return c.launch(g, p, b, model)
	}
	items := make([]menu.MenuItem, 0, len(models))
	for _, model := range models {
		items = append(items, menu.MenuItem{Label: model, Action: func() menu.Result { return c.launch(g, p, b, model) }})
	}
	return menu.Result{Next: &menu.Menu{Title: "Choose a default model for " + name + " via " + p.DisplayName() + ":", Items: items}}
}

func (c *Client) launch(g *config.Global, p config.ProviderInfo, b backend, model string) menu.Result {
	bin := clients.FindBinary(binaryName, c.CommonPaths())
	if bin == "" {
		bin = binaryName
	}
	extPath, cleanup, err := writeProviderExtension(g.ApertureHost, p, b)
	if err != nil {
		return errorResult("Failed to write " + name + " provider extension: " + err.Error())
	}
	args := buildArgs(extPath, p.ID, model, g.Settings.YoloMode)
	_ = g.RecordLaunch(config.LaunchState{
		LastClientName: name, LastBackendType: b.id, LastProviderID: p.ID, LastModel: model,
	})
	cmd := clients.Launch(clients.LaunchSpec{Binary: bin, Args: args, Cleanup: cleanup, Debug: g.Debug})
	return menu.Result{Cmd: cmd, PopOnDone: true}
}

func buildArgs(extPath, providerID, model string, yolo bool) []string {
	args := []string{"-e", extPath}
	if model != "" {
		args = append(args, "--model", ompModelRef(providerID, model))
	}
	if yolo {
		args = append(args, "--auto-approve")
	}
	return args
}

func resolveReplay(g *config.Global) (config.ProviderInfo, backend, string, bool) {
	if g.LastLaunch.LastClientName != name {
		return config.ProviderInfo{}, backend{}, "", false
	}
	prov, ok := g.Provider(g.LastLaunch.LastProviderID)
	if !ok {
		return config.ProviderInfo{}, backend{}, "", false
	}
	b, ok := backendByID(g.LastLaunch.LastBackendType)
	if !ok || len(prov.Models) == 0 || !providerSupports(prov, b) {
		return config.ProviderInfo{}, backend{}, "", false
	}
	model := g.LastLaunch.LastModel
	if model != "" && !slices.Contains(fqnModels(prov), model) {
		return config.ProviderInfo{}, backend{}, "", false
	}
	return prov, b, model, true
}

// Replay implements clients.Client.
func (c *Client) Replay(g *config.Global) tea.Cmd {
	if !c.IsInstalled() {
		return nil
	}
	prov, b, model, ok := resolveReplay(g)
	if !ok {
		return nil
	}
	return c.launch(g, prov, b, model).Cmd
}

// QuickSelectLabel implements clients.Client.
func (c *Client) QuickSelectLabel(g *config.Global) string {
	prov, _ := g.Provider(g.LastLaunch.LastProviderID)
	label := name + " via " + prov.DisplayName()
	if b, ok := backendByID(g.LastLaunch.LastBackendType); ok {
		label += " - " + b.displayName
	}
	if g.LastLaunch.LastModel != "" {
		label += " - " + g.LastLaunch.LastModel
	}
	return label
}

func compatibleProviders(all []config.ProviderInfo) []config.ProviderInfo {
	var out []config.ProviderInfo
	for _, p := range all {
		if len(p.Models) > 0 && len(backendsFor(p)) > 0 {
			out = append(out, p)
		}
	}
	return out
}

func backendsFor(p config.ProviderInfo) []backend {
	var out []backend
	for _, b := range backends {
		if providerSupports(p, b) {
			out = append(out, b)
		}
	}
	return out
}

func providerSupports(p config.ProviderInfo, b backend) bool {
	for _, key := range b.compatKeys {
		if p.Compatibility[key] {
			return true
		}
	}
	return false
}

func backendByID(id string) (backend, bool) {
	idx := slices.IndexFunc(backends, func(b backend) bool { return b.id == id })
	if idx < 0 {
		return backend{}, false
	}
	return backends[idx], true
}

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
