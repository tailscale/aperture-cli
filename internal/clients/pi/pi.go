// Package pi is the Pi coding agent client. Pi has no environment variable
// for a custom API base URL — routing is expressed as a provider definition,
// either in ~/.pi/agent/models.json or through an extension that calls
// pi.registerProvider(). This client writes a per-launch extension and loads
// it with `pi -e`, which leaves the user's own pi config directory (settings,
// logins, session history) untouched. See extension.go for why.
//
// Pi speaks four wire protocols that Aperture serves: OpenAI Chat
// Completions, OpenAI Responses, Anthropic Messages, and Google Generative
// AI (Vertex), so the menu flow is provider, then backend, then model.
package pi

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

// Client is the Pi client.
type Client struct{}

const (
	name       = "Pi"
	binaryName = "pi"

	installCmd   = "npm install -g --ignore-scripts @earendil-works/pi-coding-agent"
	uninstallCmd = "npm uninstall -g @earendil-works/pi-coding-agent"
)

// backend is one Pi wire protocol paired with the Aperture compatibility key
// a provider must set to serve it.
type backend struct {
	id          string
	displayName string
	// api is the value Pi expects in a provider definition's "api" field.
	api string
	// compatKeys are the Aperture keys that satisfy this backend; a provider
	// matches if any one is set.
	compatKeys []string
}

// backends is ordered most-preferred first, which is also the order the
// backend menu shows. Bedrock is absent on purpose: Pi's
// bedrock-converse-stream API type loads from a provider definition but
// fails at request time against Aperture, so offering it would only produce
// a confusing runtime error.
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
func (c *Client) IsInstalled() bool {
	return clients.IsInstalled(binaryName, c.CommonPaths())
}

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
			// Split into separate arguments: there is no shell here.
			return exec.Command("npm", "uninstall", "-g", "@earendil-works/pi-coding-agent").Run()
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
		return errorResult("No providers support " + name + ".")
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

	extPath, cleanup, err := writeProviderExtension(g.ApertureHost, p, b)
	if err != nil {
		return errorResult("Failed to write " + name + " provider extension: " + err.Error())
	}

	args := buildArgs(extPath, p.ID, model)

	_ = g.RecordLaunch(config.LaunchState{
		LastClientName:  name,
		LastBackendType: b.id,
		LastProviderID:  p.ID,
		LastModel:       model,
	})

	cmd := clients.Launch(clients.LaunchSpec{
		Binary:  bin,
		Args:    args,
		Cleanup: cleanup,
		Debug:   g.Debug,
	})
	return menu.Result{Cmd: cmd, PopOnDone: true}
}

// buildArgs assembles Pi's command line: load the generated extension, and
// preselect the model when the user chose one. Routing lives entirely in the
// extension, so no environment variables are set.
//
// Nothing here honors g.Settings.YoloMode, and nothing should. Pi ships no
// sandbox and never prompts before running a tool, so it has no
// skip-permissions flag to pass. Its --approve/-a flag looks like one but
// governs whether project-local .pi files are trusted, which is unrelated to
// tool approval and not the user's intent when they enable yolo mode.
func buildArgs(extPath, providerID, model string) []string {
	args := []string{"-e", extPath}
	if model != "" {
		args = append(args, "--model", piModelRef(providerID, model))
	}
	return args
}

// resolveReplay reports whether g.LastLaunch still describes a launch this
// client can repeat, returning the provider, backend, and model to use.
//
// Every staleness check except the binary-installed one lives here so it can
// be tested directly. Driving Replay instead would prove very little: Replay
// returns nil at !IsInstalled() before reaching any of this, so on a machine
// without pi — including CI, which installs no agents — such a test passes
// even if the checks below are deleted.
func resolveReplay(g *config.Global) (config.ProviderInfo, backend, string, bool) {
	if g.LastLaunch.LastClientName != name {
		return config.ProviderInfo{}, backend{}, "", false
	}
	// The provider must still exist in the freshly fetched list.
	prov, ok := g.Provider(g.LastLaunch.LastProviderID)
	if !ok {
		return config.ProviderInfo{}, backend{}, "", false
	}
	// The recorded backend must still be one we offer.
	b, ok := backendByID(g.LastLaunch.LastBackendType)
	if !ok {
		return config.ProviderInfo{}, backend{}, "", false
	}
	// The provider must still serve that backend's protocol.
	if !providerSupports(prov, b) {
		return config.ProviderInfo{}, backend{}, "", false
	}
	// The recorded model must still be offered by that provider.
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
	res := c.launch(g, prov, b, model)
	return res.Cmd
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
		if len(backendsFor(p)) > 0 {
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
	for _, k := range b.compatKeys {
		if p.Compatibility[k] {
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
