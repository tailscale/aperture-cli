// Package cli is aperture's headless command surface: the cobra tree that
// configures settings, serves bridges and launches clients without the TUI.
// A bare invocation still starts the TUI. The grammar and its output rules
// are the command-line contract in docs/specs/connection-contracts.md.
package cli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
)

// Execute runs the command line. version is what --version prints. runTUI
// starts the interactive launcher a bare invocation gets, with the endpoint,
// bridge and debug selections resolved from flags and environment.
func Execute(args []string, version string, runTUI func(endpoint, bridge string, debug bool) error) error {
	root := rootCommand(version, runTUI)
	clientNames := make([]string, 0, 8)
	for _, c := range clients.All(nil) {
		if name := c.BinaryName(); name != "" {
			clientNames = append(clientNames, name)
		}
	}
	rewritten, deprecated := gnuForm(args, longFlags, clientNames)
	warnDeprecated(root.ErrOrStderr(), deprecated)
	if len(deprecated) > 0 {
		slog.Warn("deprecated single-dash flag form used", "flags", deprecated)
	}
	// --version is answered before dispatch: a version flag with a client
	// command after it would otherwise be swallowed into the client's tail.
	if versionRequested(rewritten, clientNames) {
		fmt.Fprintln(root.OutOrStdout(), version)
		return nil
	}
	root.SetArgs(rewritten)
	return root.Execute()
}

// rootCommand builds the aperture command tree.
func rootCommand(version string, runTUI func(endpoint, bridge string, debug bool) error) *cobra.Command {
	root := &cobra.Command{
		Use:   "aperture",
		Short: "Point AI coding agents at an Aperture",
		Long: `aperture launches AI coding agents against an Aperture, directly or
through an embedded tailnet bridge. With no command it opens the
interactive launcher.

The single-dash long flags (-endpoint) are deprecated and will be removed
in a future release: use the GNU form (--endpoint) or the shorthand (-e).`,
		Example: `  aperture
  aperture --endpoint http://ai
  aperture bridge add work --port 8991
  aperture endpoint add e1 --bridge work http://ai
  aperture serve --bridge work`,
		SilenceErrors: true,
		// The TUI owns the terminal, so usage on a root error would paint
		// over nothing anyone reads. Subcommand errors keep their usage.
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			debug, _ := cmd.Flags().GetBool("debug")
			return runTUI(
				flagOrEnv(cmd, "endpoint", "APERTURE_ENDPOINT"),
				flagOrEnv(cmd, "bridge", "APERTURE_BRIDGE"),
				debug,
			)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true

	f := root.PersistentFlags()
	f.StringP("endpoint", "e", "", "Aperture URL or saved endpoint name ($APERTURE_ENDPOINT)")
	f.StringP("bridge", "b", "", "connect through the bridge with this name ($APERTURE_BRIDGE)")
	f.StringP("config", "c", "", "read and write this settings file ($APERTURE_CONFIG)")
	f.BoolP("debug", "d", false, "bridge diagnostics and launch environment on stderr")
	root.Flags().BoolP("version", "v", false, "print version and exit")

	// The settings file must be chosen before anything reads it, and every
	// subcommand reads it.
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if path := flagOrEnv(cmd, "config", "APERTURE_CONFIG"); path != "" {
			config.UseSettingsFile(path)
		}
	}

	root.AddCommand(bridgeCommand(), endpointCommand(), serveCommand())
	for _, c := range clients.All(nil) {
		if c.BinaryName() != "" {
			root.AddCommand(clientCommand(c))
		}
	}
	return root
}

// flagOrEnv returns the flag's value when the flag was passed, even empty,
// and otherwise the environment variable key. The flag wins so a one-off run
// can override the shell it started in: --bridge= means no bridge.
func flagOrEnv(cmd *cobra.Command, name, key string) string {
	if cmd.Flags().Changed(name) {
		value, _ := cmd.Flags().GetString(name)
		return value
	}
	return os.Getenv(key)
}

// loadGlobal reads settings and state for a subcommand and applies --debug.
func loadGlobal(cmd *cobra.Command) (*config.Global, error) {
	g, err := config.Load()
	if err != nil {
		return nil, err
	}
	g.Debug, _ = cmd.Flags().GetBool("debug")
	if g.Debug {
		config.RaiseRunLog()
	}
	return g, nil
}
