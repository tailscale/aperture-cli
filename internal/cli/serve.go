package cli

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/config"
)

// serveCommand builds the serve command: one Machine and one Route on a
// fixed loopback port until a signal stops it (ADR 0008). Serve is a
// foreground process; the operator's supervisor owns restart.
func serveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run a persistent bridge on a fixed port",
		Example: `  aperture serve --bridge work
  aperture serve --bridge work --port 8992
  aperture serve --bridge work --endpoint e1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			g, err := loadGlobal(cmd)
			if err != nil {
				return err
			}
			bridgeName := flagOrEnv(cmd, "bridge", "APERTURE_BRIDGE")
			if bridgeName == "" {
				return fmt.Errorf("serve needs --bridge, the name of a configured bridge")
			}
			// bridgeByName never creates: serve runs a bridge that exists,
			// and a typo must not register a device.
			b, err := bridgeByName(g, bridgeName)
			if err != nil {
				return err
			}
			port, err := servePort(flagInt(cmd, "port"), b)
			if err != nil {
				return err
			}
			remote, err := serveEndpoint(g, b, flagOrEnv(cmd, "endpoint", "APERTURE_ENDPOINT"))
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			machines := bridges.NewMachines(g.Debug)
			defer func() {
				if err := machines.Close(); err != nil {
					slog.Error("shutting down bridges", "err", err)
				}
			}()
			emit := eventPrinter(cmd, g.Debug)
			mc, err := machines.For(b)
			if err != nil {
				return err
			}
			// A signal during bring-up cancels the context. That is a clean
			// stop, not a failure: a supervisor restarting on non-zero would
			// flap a serve the operator asked to stop.
			if err := mc.Open(ctx, emit); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			route, err := mc.RouteTo(ctx, remote, port, emit)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "serving %s through %s on %s\n", remote, b.Name, route.LocalURL)
			<-ctx.Done()
			fmt.Fprintln(cmd.ErrOrStderr(), "shutting down")
			return nil
		},
	}
	cmd.Flags().IntP("port", "p", 0, "loopback port to bind, over the bridge's stored port")
	return cmd
}

// flagInt reads an integer flag. Zero means unset: a port of zero is never
// a real choice, so --port=0 behaves as if the flag was not passed.
func flagInt(cmd *cobra.Command, name string) int {
	value, _ := cmd.Flags().GetInt(name)
	return value
}

// servePort decides the port serve binds: the flag, then the bridge's
// stored port. Neither is an error, because a random port is exactly what
// serve exists to avoid (ADR 0008).
func servePort(flagPort int, b config.Bridge) (int, error) {
	if flagPort != 0 {
		if flagPort < 0 || flagPort > 65535 {
			return 0, fmt.Errorf("port %d is outside 1-65535", flagPort)
		}
		return flagPort, nil
	}
	if b.Port != 0 {
		return b.Port, nil
	}
	return 0, fmt.Errorf("bridge %s has no port; pass --port or set one with bridge add", b.Name)
}

// serveEndpoint decides the URL serve fronts. A name resolves to the saved
// endpoint's URL; an endpoint that goes through another bridge is a
// contradiction, because serve's Route runs on this bridge's Machine.
func serveEndpoint(g *config.Global, b config.Bridge, value string) (string, error) {
	if value == "" {
		return config.DefaultLocation, nil
	}
	if named, ok := g.Settings.EndpointNamed(value); ok {
		if bridged, isBridged := named.(config.BridgeEndpoint); isBridged && bridged.BridgeID() != b.ID {
			return "", fmt.Errorf("endpoint %q goes through another bridge", value)
		}
		return named.URL(), nil
	}
	return config.ParseEndpointURL(value)
}
