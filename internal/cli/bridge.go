package cli

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/connection"
)

// bridgeCommand builds the bridge command group. The group configures
// bridges and never runs one: the listener is serve (ADR 0008).
func bridgeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bridge",
		Short: "Configure tailnet bridges",
		Example: `  aperture bridge add work --port 8991
  aperture bridge list
  aperture bridge describe work
  aperture bridge delete work`,
	}
	add := &cobra.Command{
		Use:     "add <name>",
		Short:   "Add a bridge",
		Example: "  aperture bridge add work --port 8991",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			g, err := loadGlobal(cmd)
			if err != nil {
				return err
			}
			port, _ := cmd.Flags().GetInt("port")
			b, err := g.AddBridge(args[0], port)
			if err != nil {
				return err
			}
			writeBridges(cmd, g, []config.Bridge{b})
			return nil
		},
	}
	add.Flags().IntP("port", "p", 0, "fixed loopback port serve binds for this bridge")
	del := &cobra.Command{
		Use:   "delete <name>",
		Short: "Log out the bridge's machines and remove it",
		Example: `  aperture bridge delete work
  aperture bridge delete work --force`,
		Long: `Log out the bridge's machines and remove it from settings.

The logout can take a minute; the tailnet answers slowly. A bridge an
endpoint still uses, or one another aperture process is running, is
refused with the reason named. Pass --force to remove the bridge and any
endpoints connecting through it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			g, err := loadGlobal(cmd)
			if err != nil {
				return err
			}
			b, err := bridgeByName(g, args[0])
			if err != nil {
				return err
			}
			force, _ := cmd.Flags().GetBool("force")
			if !force {
				if err := bridges.CheckRemovable(g, b, nil); err != nil {
					return err
				}
			}
			if force || bridges.WillDestroyMachine(g, b, nil) {
				fmt.Fprintf(cmd.ErrOrStderr(), "logging out every machine slot of %s; this can take a minute\n", b.Name)
				machines := bridges.NewMachines(g.Debug)
				defer machines.Close()
				if err := machines.Destroy(cmd.Context(), b, eventPrinter(cmd, g.Debug)); err != nil {
					return err
				}
			}
			if force {
				for _, ep := range bridges.EndpointsThroughBridge(g, b.ID) {
					_ = g.RemoveEndpoint(ep)
				}
			}
			return g.RemoveBridge(b.ID)
		},
	}
	del.Flags().BoolP("force", "f", false, "force removal even if endpoints still connect through this bridge")

	cmd.AddCommand(
		add,
		&cobra.Command{
			Use:     "list",
			Short:   "List the configured bridges",
			Example: "  aperture bridge list",
			Args:    cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true
				g, err := loadGlobal(cmd)
				if err != nil {
					return err
				}
				writeBridges(cmd, g, g.Settings.Bridges)
				return nil
			},
		},
		&cobra.Command{
			Use:     "describe <name>",
			Short:   "Show one bridge and its machine state on disk",
			Example: "  aperture bridge describe work",
			Args:    cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true
				g, err := loadGlobal(cmd)
				if err != nil {
					return err
				}
				b, err := bridgeByName(g, args[0])
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "name: %s\nid: %s\n", b.Name, b.ID)
				if b.Port != 0 {
					fmt.Fprintf(out, "port: %d\n", b.Port)
				}
				if b.Tailnet != "" {
					fmt.Fprintf(out, "tailnet: %s\n", b.Tailnet)
				}
				slots, err := config.BridgeStateSlots(b.ID)
				if err != nil {
					return err
				}
				if len(slots) == 0 {
					fmt.Fprintln(out, "machine slots on disk: none")
				} else {
					numbers := make([]string, 0, len(slots))
					for _, slot := range slots {
						numbers = append(numbers, strconv.Itoa(slot))
					}
					fmt.Fprintf(out, "machine slots on disk: %s\n", strings.Join(numbers, ", "))
				}
				return nil
			},
		},
		del,
	)
	return cmd
}

// bridgeByName returns the bridge called name, matched without case. Unlike
// bridge creation through the launcher it never creates: a command that
// manages bridges must not make one from a typo.
func bridgeByName(g *config.Global, name string) (config.Bridge, error) {
	var matched []config.Bridge
	for _, b := range g.Settings.Bridges {
		if strings.EqualFold(b.Name, name) {
			matched = append(matched, b)
		}
	}
	switch len(matched) {
	case 0:
		return config.Bridge{}, fmt.Errorf("no bridge called %q", name)
	case 1:
		return matched[0], nil
	}
	return config.Bridge{}, fmt.Errorf("%d bridges are called %q; fix the duplicate in the settings file", len(matched), name)
}

// writeBridges prints bridges as tabbed columns. A missing port or tailnet
// is a dash, so scripts can read fixed columns.
func writeBridges(cmd *cobra.Command, g *config.Global, list []config.Bridge) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tPORT\tTAILNET")
	for _, b := range list {
		port := "-"
		if b.Port != 0 {
			port = strconv.Itoa(b.Port)
		}
		tailnet := b.Tailnet
		if tailnet == "" {
			tailnet = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", b.Name, b.ID, port, tailnet)
	}
	w.Flush()
}

// eventPrinter reports a long bridge operation to the user: login links
// always, notes only under --debug. The link goes to stderr and never the
// run log, the same rule the TUI follows.
func eventPrinter(cmd *cobra.Command, debug bool) func(connection.Event) {
	return func(ev connection.Event) {
		switch {
		case ev.Link != nil:
			fmt.Fprintf(cmd.ErrOrStderr(), "log in to continue: %s\n", ev.Link.String())
		case ev.Note != "" && debug:
			fmt.Fprintln(cmd.ErrOrStderr(), ev.Note)
		}
	}
}
