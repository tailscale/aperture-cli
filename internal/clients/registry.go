package clients

import (
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/menu"
)

// Client is one AI coding agent that the launcher can install and launch.
// Each client lives in its own sub-package and is wholly responsible for
// its own provider/backend/model flow, env generation, config writing,
// install and uninstall — all of which are expressed through the MenuItem
// returned from Menu() plus the InstallPlan / UninstallPlan.
type Client interface {
	// Name is the user-visible display name (e.g. "Claude Code").
	Name() string

	// BinaryName is the executable name checked against $PATH. Empty for
	// clients that are not a CLI binary (e.g. desktop apps).
	BinaryName() string

	// CommonPaths returns absolute paths where the binary may live outside
	// of PATH (e.g. "~/.local/bin/claude"). Used as a fallback by
	// FindBinary.
	CommonPaths() []string

	// IsInstalled reports whether the client is available locally. The
	// default implementation is clients.IsInstalled(BinaryName, CommonPaths).
	IsInstalled() bool

	// Install describes how to install the client. May read g for
	// host-dependent setup (e.g. writing platform config before download).
	Install(g *config.Global) InstallPlan

	// Uninstall describes how to uninstall the client.
	Uninstall() UninstallPlan

	// Menu returns the root menu item shown in the client picker. Its
	// Action kicks off the client's own sub-menu flow (provider → backend →
	// model → launch).
	Menu(g *config.Global) menu.MenuItem

	// Replay attempts to re-launch the client using the last-used selection
	// stored in g.LastLaunch. Returns nil if the state is stale (binary
	// missing, provider gone from g.Providers, model no longer listed).
	Replay(g *config.Global) tea.Cmd

	// QuickSelectLabel is the display text for the [0] quick-select row
	// when Replay would succeed.
	QuickSelectLabel(g *config.Global) string
}

// InstallPlan describes how to install a client.
type InstallPlan struct {
	// Hint is shown to the user before confirming; e.g.
	// "curl -fsSL https://claude.ai/install.sh | bash".
	Hint string
	// Run returns the command to execute on confirmation. If nil, the install
	// is manual-only: the TUI shows Hint and does nothing.
	Run func() (*exec.Cmd, error)
}

// UninstallPlan describes how to uninstall a client.
type UninstallPlan struct {
	// Hint is shown to the user before confirming.
	Hint string
	// Run performs the uninstall. If nil, uninstall is disabled.
	Run func() error
}

// registered holds the set of clients, populated by init() in each sub-package
// via Register.
var registered []Client

// Register adds a client to the registry. Intended to be called from a
// sub-package's init(). Order of registration determines display order.
func Register(c Client) {
	registered = append(registered, c)
}

// All returns every registered client. The g argument is accepted so callers
// always have the global state at hand; it is not currently used for
// filtering since every client decides its own menu behavior when invoked.
func All(g *config.Global) []Client {
	out := make([]Client, len(registered))
	copy(out, registered)
	return out
}
