package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"

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

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	if buildVersion == "v0.0.0-dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		buildVersion = info.Main.Version
	}

	// Only fill in VCS info when ldflags haven't already set these values.
	if buildCommit != "unknown" {
		return
	}

	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				buildCommit = s.Value[:7]
			}
		case "vcs.time":
			if buildDate == "unknown" {
				buildDate = s.Value
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if dirty && buildCommit != "unknown" {
		buildCommit += "-dirty"
	}
}

func main() {
	flag.Parse()

	if *flagVersion {
		if buildCommit != "unknown" {
			fmt.Printf("%s (%s, %s)\n", buildVersion, buildCommit, buildDate)
		} else {
			fmt.Println(buildVersion)
		}
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
