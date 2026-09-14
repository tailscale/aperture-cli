package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tailscale/aperture-cli/internal/clients"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/skills"
)

// runSkills handles "aperture skills <subcommand>" and returns the process exit
// code.
func runSkills(args []string) int {
	if len(args) == 0 {
		skillsUsage()
		return 2
	}

	switch args[0] {
	case "install":
		return runSkillsInstall(args[1:])
	case "-h", "--help", "help":
		skillsUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "aperture skills: unknown subcommand %q\n\n", args[0])
		skillsUsage()
		return 2
	}
}

func skillsUsage() {
	fmt.Fprint(os.Stderr, `Usage: aperture skills <subcommand>

Subcommands:
  install   Generate the aperture skill and install it for the agents on this machine

Run "aperture skills install -h" for the install flags.
`)
}

// runSkillsInstall generates the skill and installs it, reporting what was
// written.
func runSkillsInstall(args []string) int {
	fs := flag.NewFlagSet("aperture skills install", flag.ContinueOnError)
	project := fs.Bool("project", false, "install into the current directory instead of the home directory")
	force := fs.Bool("force", false, "overwrite skill directories owned by another tool")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	g, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading launcher config: %v\n", err)
		return 1
	}

	var agents []skills.AgentInfo
	for _, c := range clients.All(g) {
		agents = append(agents, skills.AgentInfo{
			Name:        c.Name(),
			Binary:      c.BinaryName(),
			InstallHint: c.Install(g).Hint,
		})
	}

	// Best effort: the skill falls back to describing the per-OS locations
	// when the config directory cannot be resolved.
	var configDir string
	if dir, err := os.UserConfigDir(); err == nil {
		configDir = filepath.Join(dir, "aperture")
	}

	res, err := skills.Install(skills.Options{
		Version: buildVersion,
		Skills: skills.Content(skills.ContentParams{
			Version:   buildVersion,
			Endpoint:  g.ApertureHost,
			Agents:    agents,
			ConfigDir: configDir,
		}),
		Project: *project,
		Force:   *force,
	})
	if err != nil {
		// A conflict is the expected refusal, not a crash: print it plainly
		// so the user can see which directories are in the way.
		var conflict *skills.ConflictError
		if errors.As(err, &conflict) {
			fmt.Fprintln(os.Stderr, conflict.Error())
			return 1
		}
		fmt.Fprintf(os.Stderr, "installing skill: %v\n", err)
		return 1
	}

	fmt.Printf("Installed %d skills:\n", len(res.Skills))
	for _, s := range res.Skills {
		fmt.Printf("  %s\n    %s\n", s.Name, s.Canonical)
		for _, l := range s.Linked {
			fmt.Printf("    linked into %s: %s\n", l.Agent, l.Path)
		}
	}
	for _, path := range res.Pruned {
		fmt.Printf("  pruned %s\n", path)
	}
	for _, c := range res.Overwrote {
		fmt.Printf("  overwrote %s (was owned by %s)\n", c.Path, c.Owner)
	}
	return 0
}
