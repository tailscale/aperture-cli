package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
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

Aperture flags here are the GNU form and the single-letter shorthands:
--endpoint/-e, --bridge/-b, --provider/-p, --model/-m, --config/-c,
--debug/-d. A single-dash long flag is ` + binary + `'s own in this tail.
The first flag aperture does not know, or --, passes the rest to ` + binary + ` verbatim.`,
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
			if g.Debug {
				config.RaiseRunLog()
			}

			ep, err := config.EndpointFromFlags(g, sel.endpoint, sel.bridge)
			if err != nil {
				return err
			}
			// A non-default settings file is a trust boundary: say where the
			// configuration came from, where the client is going and whether
			// its permission prompts are off, since a headless run has no
			// screen to show any of it.
			if path := config.SettingsFileOverride(); path != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "settings from %s; connecting to %s; yolo mode %v\n", path, ep.URL(), g.Settings.YoloMode)
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
			runErr := runChild(ctx, child)
			if spec.Cleanup != nil {
				spec.Cleanup()
			}
			return runErr
		},
	}
}

// runChild starts child and waits for it, forwarding ctx's cancellation as
// a SIGTERM so a signal sent to aperture itself reaches the client too:
// signal.NotifyContext's handler consumes the signal on aperture's process
// alone, and nothing else would propagate it to an already-running child.
// The child's own exit status is preserved either way.
func runChild(ctx context.Context, child *exec.Cmd) error {
	if err := child.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = child.Process.Signal(syscall.SIGTERM)
		return <-done
	}
}

// launchSelection holds the aperture flags a client command's tail parser
// consumed.
type launchSelection struct {
	endpoint, bridge, provider, model, config string
	debug                                     bool
}

// splitLaunchArgs consumes aperture's known flags from a client command's
// tail. In the tail, aperture's flags are the GNU form and the single-letter
// shorthands only: a single-dash long flag is the client's own, since
// nothing before the client command ever parsed one as aperture's (ADR
// 0007). Positional tokens pass through but do not end flag consumption.
// The first unknown flag, or --, starts verbatim passthrough to the client.
func splitLaunchArgs(args []string) (launchSelection, []string, error) {
	var sel launchSelection
	valueFlags := map[string]*string{
		"endpoint": &sel.endpoint, "e": &sel.endpoint,
		"bridge": &sel.bridge, "b": &sel.bridge,
		"provider": &sel.provider, "p": &sel.provider,
		"model": &sel.model, "m": &sel.model,
		"config": &sel.config, "c": &sel.config,
	}
	var passthrough []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return sel, append(passthrough, args[i+1:]...), nil
		}
		if arg == "" || arg == "-" || !strings.HasPrefix(arg, "-") {
			// A positional passes through; aperture keeps consuming its own
			// flags past it, anywhere in the tail.
			passthrough = append(passthrough, arg)
			continue
		}
		gnu := strings.HasPrefix(arg, "--")
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		if !gnu && len(name) > 1 {
			// A single-dash long flag in the tail is the client's own.
			return sel, append(passthrough, args[i:]...), nil
		}
		if name == "debug" || name == "d" {
			on := true
			if hasValue {
				parsed, err := strconv.ParseBool(value)
				if err != nil {
					// Not a bool form of ours: the token belongs to the client.
					return sel, append(passthrough, args[i:]...), nil
				}
				on = parsed
			}
			sel.debug = on
			continue
		}
		target, ok := valueFlags[name]
		if !ok {
			return sel, append(passthrough, args[i:]...), nil
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
	return sel, passthrough, nil
}
