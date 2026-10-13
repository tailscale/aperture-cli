package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/config"
)

// endpointCommand builds the endpoint command group.
func endpointCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "endpoint",
		Short: "Configure Aperture endpoints",
		Example: `  aperture endpoint add e1 --bridge work http://ai
  aperture endpoint list
  aperture endpoint delete e1`,
	}
	add := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Save an endpoint under a name",
		Example: `  aperture endpoint add e1 http://ai
  aperture endpoint add e1 --bridge work http://ai`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			g, err := loadGlobal(cmd)
			if err != nil {
				return err
			}
			bridge := ""
			if cmd.Flags().Changed("bridge") {
				bridge, _ = cmd.Flags().GetString("bridge")
			}
			ep, err := config.EndpointFromFlags(g, args[1], bridge)
			if err != nil {
				return err
			}
			if err := g.NameEndpoint(ep, args[0]); err != nil {
				return err
			}
			writeEndpoints(cmd, g, []config.EndpointEntry{{Endpoint: ep, Name: args[0]}})
			return nil
		},
	}
	cmd.AddCommand(
		add,
		&cobra.Command{
			Use:     "list",
			Short:   "List the configured endpoints",
			Example: "  aperture endpoint list",
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true
				g, err := loadGlobal(cmd)
				if err != nil {
					return err
				}
				writeEndpoints(cmd, g, g.Settings.Endpoints)
				return nil
			},
		},
		&cobra.Command{
			Use:   "delete <name>",
			Short: "Remove a named endpoint",
			Example: `  aperture endpoint delete e1

The active endpoint stays: connect to another one first. A bridge the
endpoint used stays until bridge delete.`,
			Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true
				g, err := loadGlobal(cmd)
				if err != nil {
					return err
				}
				ep, ok := g.Settings.EndpointNamed(args[0])
				if !ok {
					return fmt.Errorf("no endpoint called %q", args[0])
				}
				if err := bridges.CheckRemovable(g, config.Bridge{}, ep); err != nil {
					return err
				}
				return g.RemoveEndpoint(ep)
			},
		},
	)
	return cmd
}

// writeEndpoints prints endpoint entries as tabbed columns. A missing name
// or bridge is a dash, so scripts can read fixed columns.
func writeEndpoints(cmd *cobra.Command, g *config.Global, entries []config.EndpointEntry) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tURL\tBRIDGE")
	for _, entry := range entries {
		name := entry.Name
		if name == "" {
			name = "-"
		}
		bridge := "-"
		if bridged, ok := entry.Endpoint.(config.BridgeEndpoint); ok {
			bridge = bridged.BridgeID()
			if b, ok := g.Bridge(bridged.BridgeID()); ok {
				bridge = b.Name
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, entry.Endpoint.URL(), bridge)
	}
	w.Flush()
}
