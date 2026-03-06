package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/profiles"
	"github.com/tailscale/aperture-cli/internal/tui"
)

var (
	flagVersion = flag.Bool("version", false, "print version and exit")
	flagDebug   = flag.Bool("debug", false, "print env vars set before launching agent")

	buildVersion = "v0.0.0-dev"
	buildCommit  = "unknown"
	buildDate    = "unknown"
)

func main() {
	flag.Parse()

	if *flagVersion {
		fmt.Printf("%s (%s, %s)\n", buildVersion, buildCommit, buildDate)
		os.Exit(0)
	}

	settings, _ := profiles.LoadSettings()
	state, _ := profiles.LoadState()

	// Use the first saved endpoint as the active host; fall back to the default.
	host := "http://ai"
	if len(settings.Endpoints) > 0 {
		host = settings.Endpoints[0].URL
	}

	p := tea.NewProgram(tui.NewModel(host, settings, state, *flagDebug))
	if _, err := p.Run(); err != nil {
		slog.Error("launcher error", "err", err)
		os.Exit(1)
	}
}
