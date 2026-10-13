// Package claudecode is the Claude Code CLI client. It supports five routing
// flavors: Anthropic direct, AWS Bedrock, AWS Bedrock Mantle, Google Vertex,
// and z.ai. The flow per launch is provider → backend → optional model
// (skipped for Bedrock and Mantle) → Check → exec. Bedrock resolves models from
// ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL env vars derived from the
// provider's model list; Claude Code maps its own aliases for Mantle.
package claudecode

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

func init() {
	clients.Register(&Client{})
}

// Client is the Claude Code CLI client.
type Client struct {
	launchFn func(clients.LaunchSpec) tea.Cmd
}

const (
	name       = "Claude Code"
	binaryName = "claude"
)

// backend captures one of Claude Code's routing flavors.
type backend struct {
	id          string
	displayName string
	endpoints   []string
	// picksModel is false for backends where the user does not pick a
	// specific model (Bedrock: models are resolved per-tier at runtime;
	// Mantle: Claude Code maps its model aliases to Mantle model IDs).
	picksModel bool
}

var backends = []backend{
	{id: "anthropic", displayName: "Anthropic API", endpoints: []string{config.EndpointAnthropicMessages}, picksModel: true},
	{id: "bedrock", displayName: "AWS Bedrock", endpoints: []string{config.EndpointBedrockInvoke}, picksModel: false},
	{id: "mantle", displayName: "Amazon Bedrock (Mantle)", endpoints: []string{config.EndpointAnthropicMessages}, picksModel: false},
	{id: "vertex", displayName: "Google Vertex", endpoints: []string{config.EndpointVertexClaude}, picksModel: true},
	{id: "zai", displayName: "z.ai", endpoints: []string{config.EndpointAnthropicMessages}, picksModel: true},
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
		Hint: "curl -fsSL https://claude.ai/install.sh | bash",
		Run: func() (*exec.Cmd, error) {
			return exec.Command("bash", "-o", "pipefail", "-c", "curl -fsSL https://claude.ai/install.sh | bash"), nil
		},
	}
}

// Uninstall implements clients.Client.
func (c *Client) Uninstall() clients.UninstallPlan {
	return clients.UninstallPlan{
		Hint: "rm -f ~/.local/bin/claude && rm -rf ~/.local/share/claude",
		Run: func() error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			os.Remove(filepath.Join(home, ".local", "bin", "claude"))
			return os.RemoveAll(filepath.Join(home, ".local", "share", "claude"))
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
		return errorResult("No providers support Claude Code.")
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
	bs := dedupedBackendsFor(p)
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
	if !b.picksModel || len(p.Models) <= 1 {
		var m string
		if b.picksModel && len(p.Models) == 1 {
			m = p.ID + "/" + p.Models[0]
		}
		return c.launch(g, p, b, m)
	}
	models := fqnModels(p)
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

// Spec implements clients.Client.
func (c *Client) Spec(g *config.Global, provider, model string, args []string) (clients.LaunchSpec, error) {
	p, b, m, err := c.resolve(g, provider, model)
	if err != nil {
		return clients.LaunchSpec{}, err
	}
	return c.buildSpec(g, p, b, m, args)
}

// resolve picks the provider, backend and model for a headless launch: the
// flags, then LastLaunch when it still fits, then the first compatible
// provider with its first backend and default model.
func (c *Client) resolve(g *config.Global, provider, model string) (config.ProviderInfo, backend, string, error) {
	last := g.LastLaunch
	replaying := last.LastClientName == name
	if provider == "" && replaying {
		if p, ok := g.Provider(last.LastProviderID); ok && len(backendsFor(p)) > 0 {
			provider = p.ID
		}
	}
	if provider == "" {
		provs := compatibleProviders(g.Providers)
		if len(provs) == 0 {
			return config.ProviderInfo{}, backend{}, "", fmt.Errorf("no provider on this endpoint supports Claude Code")
		}
		provider = provs[0].ID
	}
	p, ok := g.Provider(provider)
	if !ok {
		return config.ProviderInfo{}, backend{}, "", fmt.Errorf("no provider %q; this endpoint offers %s", provider, listOrNone(providerIDs(compatibleProviders(g.Providers))))
	}
	choices := dedupedBackendsFor(p)
	if len(choices) == 0 {
		return config.ProviderInfo{}, backend{}, "", fmt.Errorf("provider %q does not support Claude Code; it offers %s", provider, listOrNone(endpointsOf(p)))
	}
	b := choices[0]
	sameProvider := replaying && p.ID == last.LastProviderID
	if sameProvider {
		all := backendsFor(p)
		if i := slices.IndexFunc(all, func(x backend) bool { return x.id == last.LastBackendType }); i >= 0 {
			b = all[i]
		}
	}

	if !b.picksModel {
		if model != "" {
			return config.ProviderInfo{}, backend{}, "", fmt.Errorf("%s picks its own models for Claude Code; drop --model", b.displayName)
		}
		return p, b, "", nil
	}
	models := fqnModels(p)
	if model == "" && sameProvider && slices.Contains(models, last.LastModel) {
		model = last.LastModel
	}
	if model == "" && len(models) > 0 {
		model = models[0]
	}
	return p, b, model, nil
}

// providerIDs renders provider ids for an error message.
func providerIDs(provs []config.ProviderInfo) []string {
	ids := make([]string, 0, len(provs))
	for _, p := range provs {
		ids = append(ids, p.ID)
	}
	return ids
}

// endpointsOf returns the endpoints a provider advertises, sorted.
func endpointsOf(p config.ProviderInfo) []string {
	return slices.Sorted(maps.Keys(p.SupportedEndpoints))
}

// listOrNone joins items for an error message, or says none when empty.
func listOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

// launch wraps buildSpec in the menu flow: the exec runs as a tea.Cmd and the
// menu pops when the child exits.
func (c *Client) launch(g *config.Global, p config.ProviderInfo, b backend, model string) menu.Result {
	spec, err := c.buildSpec(g, p, b, model, nil)
	if err != nil {
		return errorResult(err.Error())
	}
	launchFn := c.launchFn
	if launchFn == nil {
		launchFn = clients.Launch
	}
	return menu.Result{Cmd: launchFn(spec), PopOnDone: true}
}

// buildSpec assembles the launch for one provider, backend and model and
// records the launch state. extra passes through to the binary verbatim,
// after every flag aperture sets, for headless invocations.
func (c *Client) buildSpec(g *config.Global, p config.ProviderInfo, b backend, model string, extra []string) (clients.LaunchSpec, error) {
	if err := checkClaudeSettings(); err != nil {
		return clients.LaunchSpec{}, err
	}
	bin := clients.FindBinary(binaryName, c.CommonPaths())
	if bin == "" {
		bin = binaryName
	}
	env, err := envForBackend(g.ApertureHost, b)
	if err != nil {
		return clients.LaunchSpec{}, err
	}
	// Bedrock derives per-tier model env vars from the provider's model list.
	maps.Copy(env, tierModelEnv(b, p))
	if model != "" {
		applyModel(model, env)
	}

	var args []string
	if g.Settings.YoloMode {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, extra...)

	_ = g.RecordLaunch(config.LaunchState{
		LastClientName:  name,
		LastBackendType: b.id,
		LastProviderID:  p.ID,
		LastModel:       model,
	})

	return clients.LaunchSpec{
		Binary: bin,
		Args:   args,
		Env:    env,
		Debug:  g.Debug,
	}, nil
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
	if !backendMatches(prov, b) {
		return nil
	}
	model := g.LastLaunch.LastModel
	if b.picksModel && model != "" && !slices.Contains(fqnModels(prov), model) {
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

// compatibleProviders returns providers that can service any Claude Code
// backend, deduplicating across backends that share an endpoint.
func compatibleProviders(all []config.ProviderInfo) []config.ProviderInfo {
	var out []config.ProviderInfo
	for _, p := range all {
		if len(backendsFor(p)) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// backendsFor returns every backend the provider's endpoints support,
// without dedup (Anthropic and z.ai both take /v1/messages).
func backendsFor(p config.ProviderInfo) []backend {
	var out []backend
	for _, b := range backends {
		if backendMatches(p, b) {
			out = append(out, b)
		}
	}
	return out
}

// dedupedBackendsFor returns backends for p, dropping ones that share a
// endpoint signature with an earlier backend (keeps Anthropic, drops z.ai
// when both match /v1/messages). The user sees one row per
// functionally distinct routing option.
func dedupedBackendsFor(p config.ProviderInfo) []backend {
	raw := backendsFor(p)
	seen := make(map[string]bool)
	var out []backend
	for _, b := range raw {
		sig := strings.Join(b.endpoints, ",")
		if seen[sig] {
			continue
		}
		seen[sig] = true
		out = append(out, b)
	}
	return out
}

func backendMatches(p config.ProviderInfo, b backend) bool {
	// Mantle speaks the Anthropic Messages protocol, but Claude Code needs a
	// distinct transport mode for its authentication and model-ID semantics.
	// Upstream metadata is therefore authoritative; endpoint support alone cannot
	// distinguish Mantle from a regular Anthropic-compatible provider.
	if p.Upstream == "bedrock-mantle" {
		if b.id != "mantle" {
			return false
		}
	} else if b.id == "mantle" {
		return false
	}
	for _, endpoint := range b.endpoints {
		if p.SupportsEndpoint(endpoint) {
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

// applyModel writes the user-chosen model (FQN "provider/model") to the env.
// The provider prefix is stripped because Bedrock's URL path embeds the
// model and a stray prefix would break routing (e.g. /bedrock/model/bedrock/.../invoke).
func applyModel(fqn string, env map[string]string) {
	model := fqn
	if _, after, ok := strings.Cut(fqn, "/"); ok {
		model = after
	}
	env["ANTHROPIC_MODEL"] = model
}

// tierModelEnv derives ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL from the
// provider's model list when the backend does not pick a specific model
// (i.e. Bedrock). For z.ai, Env already sets fixed model names so we return
// nil. For all other backends this is a no-op.
func tierModelEnv(b backend, p config.ProviderInfo) map[string]string {
	if b.id != "bedrock" {
		return nil
	}
	if !backendMatches(p, b) {
		return nil
	}

	models := slices.Clone(p.Models)
	env := make(map[string]string)
	targets := []struct {
		substr string
		envKey string
	}{
		{"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"},
		{"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
		{"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
	}
	sort.Sort(sort.Reverse(sort.StringSlice(models)))
	for _, m := range models {
		lower := strings.ToLower(m)
		for _, t := range targets {
			if _, ok := env[t.envKey]; !ok && strings.Contains(lower, t.substr) {
				env[t.envKey] = m
			}
		}
	}
	return env
}

func errorResult(msg string) menu.Result {
	return menu.Result{Cmd: func() tea.Msg {
		return menu.SimpleDoneMsg{Err: errString(msg)}
	}}
}

type errString string

func (e errString) Error() string { return string(e) }
