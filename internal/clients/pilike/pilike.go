// Package pilike is the shared client for harnesses built on the Pi coding
// agent's plugin API. Pi and its forks have no environment variable for a
// custom API base URL — routing is expressed as a provider definition, either
// in the harness's own models.json or through an extension that calls
// pi.registerProvider(). This client writes a per-launch extension and loads
// it with `-e`, which leaves the user's own config directory (settings,
// logins, session history) untouched. See extension.go for why.
//
// These harnesses speak four wire protocols that Aperture serves: OpenAI Chat
// Completions, OpenAI Responses, Anthropic Messages, and Google Generative
// AI (Vertex), so the menu flow is provider, then backend, then model.
//
// Everything that differs between one harness and another lives in Variant.
// A harness that shares this plugin API belongs here as a Variant rather than
// as a forked copy of the package.
package pilike

import (
	"os/exec"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

// Variant is everything that differs between one pi-like harness and
// another. Every other part of the client is shared.
type Variant struct {
	// Name is the user-visible display name. It is also the value persisted
	// as LaunchState.LastClientName, so changing it invalidates a user's
	// recorded launch.
	Name string
	// BinaryName is the executable name looked up on PATH and under
	// CommonPaths.
	BinaryName string
	// ConfigDir is the per-client directory name passed to
	// config.ClientConfigDir, where the per-launch extension is written.
	ConfigDir string
	// InstallCmd is the shell command line that installs the harness. It is
	// shown to the user as the install hint and run through /bin/sh.
	InstallCmd string
	// UninstallArgv is the uninstall command, already split into arguments:
	// there is no shell in the uninstall path. Joined with spaces, it is
	// also the uninstall hint.
	UninstallArgv []string
	// CommonPaths returns the non-PATH locations where the binary is
	// commonly installed. Each entry is an absolute path to the binary
	// itself, not a directory: clients.FindBinary stats each one directly.
	CommonPaths func() []string
	// YoloArgs are the arguments appended when the user has enabled yolo
	// mode. It is empty for a harness with no permission prompts to skip;
	// see buildArgs for why that is not an oversight.
	YoloArgs []string
	// ExtensionHeader is the comment block placed at the top of the
	// generated extension. It must end in a newline.
	ExtensionHeader string
}

// Client is a pi-like client. The zero value is not usable; V must be set.
type Client struct {
	V Variant
}

// backend is one pi-like wire protocol paired with the Aperture endpoints a
// provider must advertise to serve it.
type backend struct {
	id          string
	displayName string
	// api is the value the harness expects in a provider definition's "api"
	// field.
	api string
	// endpoints are the Aperture endpoints that satisfy this backend; a
	// provider matches if it advertises any one of them.
	endpoints []string
}

// backends is ordered most-preferred first, which is also the order the
// backend menu shows. Bedrock is absent on purpose: the
// bedrock-converse-stream API type loads from a provider definition but
// fails at request time against Aperture, so offering it would only produce
// a confusing runtime error.
//
// The IDs are persisted as LaunchState.LastBackendType, so renaming one
// silently drops every user's recorded launch back to the full menu.
var backends = []backend{
	{id: "openai_responses", displayName: "OpenAI Responses", api: "openai-responses", endpoints: []string{config.EndpointOpenAIResponses}},
	{id: "anthropic", displayName: "Anthropic Messages", api: "anthropic-messages", endpoints: []string{config.EndpointAnthropicMessages}},
	{id: "openai_chat", displayName: "OpenAI Chat Completions", api: "openai-completions", endpoints: []string{config.EndpointOpenAIChat}},
	{id: "vertex", displayName: "Google Vertex", api: "google-generative-ai", endpoints: []string{config.EndpointVertexGemini, config.EndpointVertexClaude}},
}

// Name implements clients.Client.
func (c *Client) Name() string { return c.V.Name }

// BinaryName implements clients.Client.
func (c *Client) BinaryName() string { return c.V.BinaryName }

// CommonPaths implements clients.Client.
func (c *Client) CommonPaths() []string { return c.V.CommonPaths() }

// IsInstalled implements clients.Client.
func (c *Client) IsInstalled() bool {
	return clients.IsInstalled(c.V.BinaryName, c.CommonPaths())
}

// Install implements clients.Client.
func (c *Client) Install(_ *config.Global) clients.InstallPlan {
	return clients.InstallPlan{
		Hint: c.V.InstallCmd,
		Run: func() (*exec.Cmd, error) {
			return exec.Command("/bin/sh", "-c", c.V.InstallCmd), nil
		},
	}
}

// Uninstall implements clients.Client.
func (c *Client) Uninstall() clients.UninstallPlan {
	argv := c.V.UninstallArgv
	return clients.UninstallPlan{
		Hint: strings.Join(argv, " "),
		Run: func() error {
			// Already separate arguments: there is no shell here.
			return exec.Command(argv[0], argv[1:]...).Run()
		},
	}
}

// Menu implements clients.Client.
func (c *Client) Menu(g *config.Global) menu.MenuItem {
	return menu.MenuItem{
		Label:  c.V.Name,
		Action: func() menu.Result { return c.providerStep(g) },
	}
}

func (c *Client) providerStep(g *config.Global) menu.Result {
	provs := compatibleProviders(g.Providers)
	if len(provs) == 0 {
		return errorResult("No providers support " + c.V.Name + ".")
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
		Title: "Choose a provider for " + c.V.Name + ":",
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
		Title: "Choose a backend for " + c.V.Name + " via " + p.DisplayName() + ":",
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
		Title: "Choose a default model for " + c.V.Name + " via " + p.DisplayName() + ":",
		Items: items,
	}}
}

func (c *Client) launch(g *config.Global, p config.ProviderInfo, b backend, m string) menu.Result {
	bin := clients.FindBinary(c.V.BinaryName, c.CommonPaths())
	if bin == "" {
		bin = c.V.BinaryName
	}

	extPath, cleanup, err := writeProviderExtension(c.V, g.ApertureHost, p, b)
	if err != nil {
		return errorResult("Failed to write " + c.V.Name + " provider extension: " + err.Error())
	}

	args := buildArgs(c.V, extPath, p.ID, m, g.Settings.YoloMode)

	_ = g.RecordLaunch(config.LaunchState{
		LastClientName:  c.V.Name,
		LastBackendType: b.id,
		LastProviderID:  p.ID,
		LastModel:       m,
	})

	cmd := clients.Launch(clients.LaunchSpec{
		Binary:  bin,
		Args:    args,
		Cleanup: cleanup,
		Debug:   g.Debug,
	})
	return menu.Result{Cmd: cmd, PopOnDone: true}
}

// buildArgs assembles the harness's command line: load the generated
// extension, and preselect the model when the user chose one. Routing lives
// entirely in the extension, so no environment variables are set.
//
// Yolo mode appends v.YoloArgs, which is empty for a harness that has
// nothing to skip. Pi itself is such a harness: it ships no sandbox and never
// prompts before running a tool, so it has no skip-permissions flag to pass.
// Its --approve/-a flag looks like one but governs whether project-local .pi
// files are trusted, which is unrelated to tool approval and not the user's
// intent when they enable yolo mode. A fork that does prompt before running a
// tool — Oh My Pi, whose --auto-approve is a real approval bypass — sets
// YoloArgs to that flag. Do not fill YoloArgs with a flag that merely sounds
// like one.
func buildArgs(v Variant, extPath, providerID, m string, yolo bool) []string {
	args := []string{"-e", extPath}
	if m != "" {
		args = append(args, "--model", modelRef(providerID, m))
	}
	if yolo {
		args = append(args, v.YoloArgs...)
	}
	return args
}

// resolveReplay reports whether g.LastLaunch still describes a launch this
// client can repeat, returning the provider, backend, and model to use.
//
// Every staleness check except the binary-installed one lives here so it can
// be tested directly. Driving Replay instead would prove very little: Replay
// returns nil at !IsInstalled() before reaching any of this, so on a machine
// without the harness — including CI, which installs no agents — such a test
// passes even if the checks below are deleted.
func resolveReplay(v Variant, g *config.Global) (config.ProviderInfo, backend, string, bool) {
	if g.LastLaunch.LastClientName != v.Name {
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
	if len(prov.Models) == 0 || !providerSupports(prov, b) {
		return config.ProviderInfo{}, backend{}, "", false
	}
	// The recorded model must still be offered by that provider.
	m := g.LastLaunch.LastModel
	if m != "" && !slices.Contains(fqnModels(prov), m) {
		return config.ProviderInfo{}, backend{}, "", false
	}
	return prov, b, m, true
}

// Replay implements clients.Client.
func (c *Client) Replay(g *config.Global) tea.Cmd {
	if !c.IsInstalled() {
		return nil
	}
	prov, b, m, ok := resolveReplay(c.V, g)
	if !ok {
		return nil
	}
	res := c.launch(g, prov, b, m)
	return res.Cmd
}

// QuickSelectLabel implements clients.Client.
func (c *Client) QuickSelectLabel(g *config.Global) string {
	prov, _ := g.Provider(g.LastLaunch.LastProviderID)
	label := c.V.Name + " via " + prov.DisplayName()
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
	for _, endpoint := range b.endpoints {
		if p.SupportsEndpoint(endpoint) {
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
