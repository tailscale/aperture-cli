package cli

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
)

// clientCommand builds the launch command for one registered client. Flag
// parsing is the command's own: aperture consumes its known flags, and the
// first unknown flag or -- starts verbatim passthrough to the client
// (ADR 0007), so a shell alias for the binary works unchanged.
func clientCommand(c clients.Client) *cobra.Command {
	binary := c.BinaryName()
	return &cobra.Command{
		Use:   binary + " [aperture flags] [-- client args]",
		Short: "Launch " + c.Name() + " against an Aperture",
		Long: `Launch ` + c.Name() + ` against an Aperture, direct or through a bridge.

Aperture flags: --endpoint/-e, --bridge/-b, --provider/-p, --model/-m,
--config/-c, --debug/-d. The first flag aperture does not know, or --,
passes the rest to ` + binary + ` verbatim.`,
		Example: "  aperture " + binary + ` --endpoint e1
  aperture ` + binary + ` --model some-model -- --search`,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			sel, extra, err := splitLaunchArgs(args)
			if err != nil {
				return err
			}
			// DisableFlagParsing keeps the persistent pre-run from seeing
			// --config, so the tail parser applies it here instead.
			if sel.config != "" {
				config.UseSettingsFile(sel.config)
			}
			g, err := config.Load()
			if err != nil {
				return err
			}
			g.Debug = sel.debug

			ep, err := config.EndpointFromFlags(g, sel.endpoint, sel.bridge)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			machines := bridges.NewMachines(g.Debug)
			defer machines.Close()
			attempt, err := bridges.BeginAttempt(g, ep, false, nil)
			if err != nil {
				return err
			}
			gw, err := attempt.Run(ctx, machines, eventPrinter(cmd, g.Debug))
			if err != nil {
				return err
			}
			if err := attempt.Commit(g, gw); err != nil {
				return err
			}

			spec, err := c.Spec(g, sel.provider, sel.model, extra)
			if err != nil {
				return err
			}
			child, err := spec.Command()
			if err != nil {
				return err
			}
			runErr := child.Run()
			if spec.Cleanup != nil {
				spec.Cleanup()
			}
			return runErr
		},
	}
}

// launchSelection holds the aperture flags a client command's tail parser
// consumed.
type launchSelection struct {
	endpoint, bridge, provider, model, config string
	debug                                     bool
}

// splitLaunchArgs consumes aperture's known flags from a client command's
// tail. The first unknown token, or --, starts verbatim passthrough to the
// client binary (ADR 0007). Deprecated single-dash long forms never reach
// here: gnuForm rewrote them before cobra ran.
func splitLaunchArgs(args []string) (launchSelection, []string, error) {
	var sel launchSelection
	valueFlags := map[string]*string{
		"endpoint": &sel.endpoint, "e": &sel.endpoint,
		"bridge": &sel.bridge, "b": &sel.bridge,
		"provider": &sel.provider, "p": &sel.provider,
		"model": &sel.model, "m": &sel.model,
		"config": &sel.config, "c": &sel.config,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return sel, args[i+1:], nil
		}
		if arg == "" || arg == "-" || !strings.HasPrefix(arg, "-") {
			return sel, args[i:], nil
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		if name == "debug" || name == "d" {
			sel.debug = true
			continue
		}
		target, ok := valueFlags[name]
		if !ok {
			return sel, args[i:], nil
		}
		if !hasValue {
			i++
			if i >= len(args) {
				return sel, nil, fmt.Errorf("flag %s needs a value", arg)
			}
			value = args[i]
		}
		*target = value
	}
	return sel, nil, nil
}
