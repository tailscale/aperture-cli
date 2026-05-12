package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/portals"
	"github.com/tailscale/aperture-cli/internal/profiles"
	"github.com/tailscale/aperture-cli/internal/tui"

	// Side-effect imports register each client with internal/clients.
	_ "github.com/tailscale/aperture-cli/internal/clients/claudecode"
	_ "github.com/tailscale/aperture-cli/internal/clients/codex"
	_ "github.com/tailscale/aperture-cli/internal/clients/gemini"
	_ "github.com/tailscale/aperture-cli/internal/clients/opencode"
)

var (
	flagVersion = flag.Bool("version", false, "print version and exit")
	flagDebug   = flag.Bool("debug", false, "print env vars set before launching agent")

	buildVersion = "B0-dev"
	buildCommit  = "unknown"
	buildDate    = "unknown"
)

func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	if buildVersion == "B0-dev" {
		if height := gitCommitHeight(); height != "" {
			buildVersion = "B" + height
		} else if info.Main.Version != "" && info.Main.Version != "(devel)" {
			buildVersion = info.Main.Version
		}
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

func gitCommitHeight() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return gitCommitHeightInDir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
	}
}

func gitCommitHeightInDir(dir string) string {
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	height := strings.TrimSpace(string(out))
	if height == "" {
		return ""
	}
	for _, r := range height {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return height
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

	g, err := config.Load()
	if err != nil {
		slog.Error("loading launcher config", "err", err)
		os.Exit(1)
	}
	g.Debug = *flagDebug

	// Register Claude Desktop on supported platforms (darwin, windows).
	profiles.RegisterIfSupported()

	portalManager := portals.NewManager(g.Debug)
	defer portalManager.Close()

	p := tea.NewProgram(tui.NewModel(g, buildVersion, portalManager))
	if _, err := p.Run(); err != nil {
		slog.Error("launcher error", "err", err)
		os.Exit(1)
	}
}
