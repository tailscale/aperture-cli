// Package gemini is the Google Gemini CLI client. It supports two routing
// flavors — Vertex AI (when a provider exposes experimental Gemini-on-Vertex
// compatibility) and the Gemini API — and writes a GEMINI_CLI_HOME whose
// settings.json selects the matching auth type for the chosen flavor.
package gemini

import (
	"fmt"
	"net/url"
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

// Client is the Gemini CLI client.
type Client struct{}

const (
	name       = "Gemini CLI"
	binaryName = "gemini"
)

// backend captures one of Gemini CLI's routing flavors.
type backend struct {
	id          string
	displayName string
	endpoint    string
	authType    string
}

var backends = []backend{
	{
		id:          "vertex",
		displayName: "Google Vertex",
		endpoint:    config.EndpointVertexGemini,
		authType:    "vertex-ai",
	},
	{
		id:          "gemini",
		displayName: "Gemini API",
		endpoint:    config.EndpointGemini,
		authType:    "gemini-api-key",
	},
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
		Hint: "npm install -g @google/gemini-cli",
		Run: func() (*exec.Cmd, error) {
			return exec.Command("/bin/sh", "-c", "npm install -g @google/gemini-cli"), nil
		},
	}
}

// Uninstall implements clients.Client.
func (c *Client) Uninstall() clients.UninstallPlan {
	return clients.UninstallPlan{
		Hint: "npm uninstall -g @google/gemini-cli",
		Run: func() error {
			return exec.Command("npm", "uninstall", "-g", "@google/gemini-cli").Run()
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
		return errorResult("No providers support Gemini CLI (Vertex or Gemini API).")
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
		return c.launch(g, p, bs[0])
	}
	items := make([]menu.MenuItem, 0, len(bs))
	for _, b := range bs {
		items = append(items, menu.MenuItem{
			Label:  b.displayName,
			Action: func() menu.Result { return c.launch(g, p, b) },
		})
	}
	return menu.Result{Next: &menu.Menu{
		Title: "Choose a backend for " + name + " via " + p.DisplayName() + ":",
		Items: items,
	}}
}

// Spec implements clients.Client. Gemini has no model menu, so model is
// passed to the CLI as --model when set and left to the CLI otherwise.
func (c *Client) Spec(g *config.Global, provider, model string, args []string) (clients.LaunchSpec, error) {
	p, b, m, err := c.resolve(g, provider, model)
	if err != nil {
		return clients.LaunchSpec{}, err
	}
	return c.buildSpec(g, p, b, m, args)
}

// resolve picks the provider, backend and model for a headless launch: the
// flags, then LastLaunch when it still fits, then the first compatible
// provider and its first backend. An empty model leaves the choice to the CLI.
func (c *Client) resolve(g *config.Global, provider, model string) (config.ProviderInfo, backend, string, error) {
	last := g.LastLaunch
	if last.LastClientName != name {
		last = config.LaunchState{}
	}
	if provider == "" {
		if p, ok := g.Provider(last.LastProviderID); ok && len(backendsFor(p)) > 0 {
			provider = p.ID
		} else if provs := compatibleProviders(g.Providers); len(provs) > 0 {
			provider = provs[0].ID
		} else {
			return config.ProviderInfo{}, backend{}, "", fmt.Errorf("no provider supports Gemini CLI (Vertex or Gemini API); the endpoint offers %s", providerIDs(g.Providers))
		}
	}
	p, ok := g.Provider(provider)
	if !ok {
		return config.ProviderInfo{}, backend{}, "", fmt.Errorf("no provider %q; the endpoint offers %s", provider, providerIDs(compatibleProviders(g.Providers)))
	}
	bs := backendsFor(p)
	if len(bs) == 0 {
		return config.ProviderInfo{}, backend{}, "", fmt.Errorf("provider %q does not serve Vertex Gemini or the Gemini API", provider)
	}
	// Only the provider that LastLaunch recorded carries its backend and model over.
	if p.ID != last.LastProviderID {
		return p, bs[0], model, nil
	}
	if model == "" {
		model = last.LastModel
	}
	b := bs[0]
	if i := slices.IndexFunc(bs, func(candidate backend) bool { return candidate.id == last.LastBackendType }); i >= 0 {
		b = bs[i]
	}
	return p, b, model, nil
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

// launch builds the menu's launch for one provider and backend. The menu
// never picks a model, so the CLI default applies.
func (c *Client) launch(g *config.Global, p config.ProviderInfo, b backend) menu.Result {
	spec, err := c.buildSpec(g, p, b, "", nil)
	if err != nil {
		return errorResult(err.Error())
	}
	return menu.Result{Cmd: clients.Launch(spec), PopOnDone: true}
}

// buildSpec assembles the launch for one provider, backend and model and
// records the launch state. extra passes through to the binary verbatim,
// after every flag aperture sets, for headless invocations.
func (c *Client) buildSpec(g *config.Global, p config.ProviderInfo, b backend, model string, extra []string) (clients.LaunchSpec, error) {
	// Gemini CLI 0.40+ refuses custom base URLs that aren't https:// with a
	// fully-qualified domain name (it allows http only for literal
	// localhost / 127.0.0.1). Aperture's default "http://ai" short hostname
	// won't work, so block the launch with a clear message rather than let
	// Gemini fail with an authentication error after start.
	if err := validateHost(g.ApertureHost); err != nil {
		return clients.LaunchSpec{}, err
	}
	bin := clients.FindBinary(binaryName, c.CommonPaths())
	if bin == "" {
		bin = binaryName
	}
	geminiHome, err := writeConfig(b.authType)
	if err != nil {
		return clients.LaunchSpec{}, fmt.Errorf("Failed to write Gemini config: %w", err)
	}

	host := strings.TrimRight(g.ApertureHost, "/")

	env := map[string]string{
		"GEMINI_CLI_HOME": geminiHome,
	}
	switch b.id {
	case "vertex":
		env["GOOGLE_VERTEX_BASE_URL"] = host
		env["GOOGLE_API_KEY"] = "not-needed"
	case "gemini":
		env["GEMINI_API_KEY"] = "not-needed"
		// Gemini CLI 0.40+ reads GOOGLE_GEMINI_BASE_URL; older versions
		// honored GEMINI_BASE_URL. Set both so we route correctly across
		// the upgrade.
		env["GEMINI_BASE_URL"] = host
		env["GOOGLE_GEMINI_BASE_URL"] = host
	}

	var args []string
	if g.Settings.YoloMode {
		args = append(args, "--yolo")
	}
	if model != "" {
		args = append(args, "--model", model)
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
	if !prov.SupportsEndpoint(b.endpoint) {
		return nil
	}
	res := c.launch(g, prov, b)
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
	return name + " via " + prov.DisplayName() + " - " + b
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
		if p.SupportsEndpoint(b.endpoint) {
			out = append(out, b)
		}
	}
	return out
}

// validateHost rejects aperture endpoints that Gemini CLI 0.40+ will refuse.
// The CLI's validator requires the base URL to be https:// and have a
// fully-qualified host (it treats a bare label like "ai" as unusable except
// when it is literal localhost / 127.0.0.1).
func validateHost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf(
			"Gemini CLI needs a valid Aperture endpoint URL.\n\n"+
				"Current: %q\n\n"+
				"Set an HTTPS endpoint with a fully-qualified domain name "+
				"(e.g. https://ai.example.com) in Settings → Aperture Endpoints.",
			raw,
		)
	}
	if u.Scheme != "https" {
		return fmt.Errorf(
			"Gemini CLI requires an HTTPS Aperture endpoint.\n\n"+
				"Current: %q\n\n"+
				"Set an https:// endpoint (e.g. https://ai.example.com) in "+
				"Settings → Aperture Endpoints.",
			raw,
		)
	}
	host := u.Hostname()
	if !strings.Contains(host, ".") {
		return fmt.Errorf(
			"Gemini CLI requires a fully-qualified domain name for its "+
				"Aperture endpoint.\n\n"+
				"Current: %q\n\n"+
				"Short hostnames like %q are rejected by Gemini CLI. Use the "+
				"full FQDN (e.g. https://ai.example.com) in Settings → "+
				"Aperture Endpoints.",
			raw, host,
		)
	}
	return nil
}

func errorResult(msg string) menu.Result {
	return menu.Result{Cmd: func() tea.Msg {
		return menu.SimpleDoneMsg{Err: errString(msg)}
	}}
}

type errString string

func (e errString) Error() string { return string(e) }
