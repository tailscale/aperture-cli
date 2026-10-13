// Package opencode is the OpenCode client. Unlike the other clients,
// OpenCode has a single abstract routing flavor: the real protocol (OpenAI
// Responses, OpenAI Chat, Anthropic Messages, Bedrock, Vertex, Gemini) is
// decided at launch time from the chosen provider's advertised endpoints. The
// Menu flow goes straight from provider selection to launch; model
// selection happens inside OpenCode itself.
package opencode

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

// supportedEndpoints is the set of Aperture endpoints OpenCode can translate
// into a working config. A provider matches if any one is advertised.
var supportedEndpoints = []string{
	config.EndpointOpenAIResponses,
	config.EndpointAnthropicMessages,
	config.EndpointOpenAIChat,
	config.EndpointVertexGemini,
	config.EndpointVertexClaude,
	config.EndpointBedrockInvoke,
	config.EndpointBedrockConverse,
	config.EndpointGemini,
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
			return exec.Command("bash", "-o", "pipefail", "-c", "curl -fsSL https://opencode.ai/install | bash"), nil
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
		return c.launch(g, provs[0])
	}
	items := make([]menu.MenuItem, 0, len(provs))
	for _, p := range provs {
		items = append(items, menu.MenuItem{
			Label:       p.DisplayName(),
			Description: p.Description,
			Action:      func() menu.Result { return c.launch(g, p) },
		})
	}
	return menu.Result{Next: &menu.Menu{
		Title: "Choose a provider for " + name + ":",
		Items: items,
	}}
}

func (c *Client) launch(g *config.Global, p config.ProviderInfo) menu.Result {
	spec, err := c.buildSpec(g, p, "", nil)
	if err != nil {
		return errorResult("Failed to write OpenCode config: " + err.Error())
	}
	return menu.Result{Cmd: clients.Launch(spec), PopOnDone: true}
}

// buildSpec writes the provider config and records the launch state. model
// becomes OpenCode's --model and extra passes through verbatim, both for
// headless invocations; the menu passes neither.
func (c *Client) buildSpec(g *config.Global, p config.ProviderInfo, model string, extra []string) (clients.LaunchSpec, error) {
	bin := clients.FindBinary(binaryName, c.CommonPaths())
	if bin == "" {
		bin = binaryName
	}
	configPath, cleanup, err := writeProviderConfig(g.ApertureHost, p)
	if err != nil {
		return clients.LaunchSpec{}, err
	}

	env := map[string]string{
		"OPENCODE_CONFIG": configPath,
	}
	// Bedrock SDK requires at least placeholder AWS credentials and region.
	if p.SupportsEndpoint(config.EndpointBedrockInvoke) || p.SupportsEndpoint(config.EndpointBedrockConverse) {
		env["AWS_ACCESS_KEY_ID"] = "not-needed"
		env["AWS_SECRET_ACCESS_KEY"] = "not-needed"
		env["AWS_REGION"] = "us-east-1"
	}

	// OpenCode has no documented yolo flag today. The provider config written
	// above conveys available models; model is OpenCode's own --model, given
	// only by a headless invocation.
	var args []string
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, extra...)

	_ = g.RecordLaunch(config.LaunchState{
		LastClientName:  name,
		LastBackendType: "openai", // historical; OpenCode's abstract backend
		LastProviderID:  p.ID,
		LastModel:       model,
	})

	return clients.LaunchSpec{
		Binary:  bin,
		Args:    args,
		Env:     env,
		Cleanup: cleanup,
		Debug:   g.Debug,
	}, nil
}

// Spec implements clients.Client. OpenCode picks its model in its own UI,
// so only the provider is resolved: a --model flag passes through verbatim,
// in the provider/model form OpenCode's own --model takes.
func (c *Client) Spec(g *config.Global, provider, model string, args []string) (clients.LaunchSpec, error) {
	p, err := c.resolveProvider(g, provider)
	if err != nil {
		return clients.LaunchSpec{}, err
	}
	spec, err := c.buildSpec(g, p, model, args)
	if err != nil {
		return clients.LaunchSpec{}, fmt.Errorf("write OpenCode config: %w", err)
	}
	return spec, nil
}

// resolveProvider picks the provider for a headless launch: the flag, then
// LastLaunch when it still fits, then the first compatible provider.
func (c *Client) resolveProvider(g *config.Global, provider string) (config.ProviderInfo, error) {
	if provider != "" {
		p, ok := g.Provider(provider)
		if !ok {
			return config.ProviderInfo{}, fmt.Errorf("no provider %q; the endpoint offers %s", provider, providerIDs(compatibleProviders(g.Providers)))
		}
		if !providerMatches(p) {
			return config.ProviderInfo{}, fmt.Errorf("provider %q advertises no endpoint OpenCode can use; the endpoint offers %s", provider, providerIDs(compatibleProviders(g.Providers)))
		}
		return p, nil
	}
	if last := g.LastLaunch; last.LastClientName == name {
		if p, ok := g.Provider(last.LastProviderID); ok && providerMatches(p) {
			return p, nil
		}
	}
	provs := compatibleProviders(g.Providers)
	if len(provs) == 0 {
		return config.ProviderInfo{}, errors.New("no providers support an OpenCode protocol")
	}
	return provs[0], nil
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
	res := c.launch(g, prov)
	return res.Cmd
}

// QuickSelectLabel implements clients.Client.
func (c *Client) QuickSelectLabel(g *config.Global) string {
	prov, _ := g.Provider(g.LastLaunch.LastProviderID)
	return name + " via " + prov.DisplayName()
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
	for _, endpoint := range supportedEndpoints {
		if p.SupportsEndpoint(endpoint) {
			return true
		}
	}
	return false
}

func errorResult(msg string) menu.Result {
	return menu.Result{Cmd: func() tea.Msg {
		return menu.SimpleDoneMsg{Err: errString(msg)}
	}}
}

type errString string

func (e errString) Error() string { return string(e) }
