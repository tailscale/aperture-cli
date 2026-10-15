// Package hermes is the Hermes Agent client. Hermes speaks OpenAI Chat
// Completions and accepts a custom endpoint through CUSTOM_BASE_URL, so this
// client configures routing entirely through environment variables and a
// provider argument without replacing the user's Hermes configuration.
package hermes

import (
	"fmt"
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

	// backendType identifies Hermes' single backend in persisted launch state.
	// Users have this string in their state.json, so it must not change.
	backendType = "openai_chat"

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
			return exec.Command("bash", "-o", "pipefail", "-c", installCmd), nil
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

// Spec implements clients.Client.
func (c *Client) Spec(g *config.Global, provider, model string, args []string) (clients.LaunchSpec, error) {
	p, m, err := c.resolve(g, provider, model)
	if err != nil {
		return clients.LaunchSpec{}, err
	}
	return c.buildSpec(g, p, m, args), nil
}

// resolve picks the provider and model for a headless launch: the flags,
// then LastLaunch when it still fits, then the first compatible provider
// and its default model.
func (c *Client) resolve(g *config.Global, provider, model string) (config.ProviderInfo, string, error) {
	compatible := compatibleProviders(g.Providers)
	if provider != "" {
		p, ok := g.Provider(provider)
		if !ok {
			return config.ProviderInfo{}, "", fmt.Errorf("no provider %q; compatible providers are %s", provider, providerIDs(compatible))
		}
		if !providerMatches(p) {
			return config.ProviderInfo{}, "", fmt.Errorf("provider %q does not support OpenAI /v1/chat/completions; compatible providers are %s", provider, providerIDs(compatible))
		}
		if model == "" {
			model = defaultModel(p)
		}
		return p, model, nil
	}
	if last := g.LastLaunch; last.LastClientName == name && last.LastBackendType == backendType {
		if p, ok := g.Provider(last.LastProviderID); ok && providerMatches(p) {
			if model == "" {
				if slices.Contains(fqnModels(p), last.LastModel) {
					model = last.LastModel
				} else {
					model = defaultModel(p)
				}
			}
			return p, model, nil
		}
	}
	if len(compatible) == 0 {
		return config.ProviderInfo{}, "", fmt.Errorf("no providers support OpenAI /v1/chat/completions")
	}
	if model == "" {
		model = defaultModel(compatible[0])
	}
	return compatible[0], model, nil
}

// defaultModel returns the provider's first model in provider_id/model_id
// form, or empty when it lists none.
func defaultModel(p config.ProviderInfo) string {
	if models := fqnModels(p); len(models) > 0 {
		return models[0]
	}
	return ""
}

// providerIDs renders provider ids for an error message.
func providerIDs(provs []config.ProviderInfo) string {
	if len(provs) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(provs))
	for _, p := range provs {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ", ")
}

func (c *Client) launch(g *config.Global, p config.ProviderInfo, model string) menu.Result {
	return menu.Result{Cmd: clients.Launch(c.buildSpec(g, p, model, nil)), PopOnDone: true}
}

// buildSpec assembles the launch for one provider and model and records the
// launch state. extra passes through to the binary verbatim, after every
// flag aperture sets, for headless invocations.
func (c *Client) buildSpec(g *config.Global, p config.ProviderInfo, model string, extra []string) clients.LaunchSpec {
	bin := clients.FindBinary(binaryName, c.CommonPaths())
	if bin == "" {
		bin = binaryName
	}
	args := append(buildArgs(g.Settings.YoloMode), extra...)
	_ = g.RecordLaunch(config.LaunchState{
		LastClientName: name, LastBackendType: backendType, LastProviderID: p.ID, LastModel: model,
	})
	return clients.LaunchSpec{Binary: bin, Args: args, Env: buildEnv(g.ApertureHost, model), Debug: g.Debug}
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
	if g.LastLaunch.LastClientName != name || g.LastLaunch.LastBackendType != backendType {
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

func providerMatches(p config.ProviderInfo) bool {
	return p.SupportsEndpoint(config.EndpointOpenAIChat)
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
