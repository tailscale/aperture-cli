package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/config"
	"github.com/tailscale/aperture-cli/internal/profiles"
	"github.com/tailscale/aperture-cli/internal/tui"

	// Side-effect imports register each client with internal/clients.
	_ "github.com/tailscale/aperture-cli/internal/clients/claudecode"
	_ "github.com/tailscale/aperture-cli/internal/clients/codex"
	_ "github.com/tailscale/aperture-cli/internal/clients/copilot"
	_ "github.com/tailscale/aperture-cli/internal/clients/gemini"
	_ "github.com/tailscale/aperture-cli/internal/clients/hermes"
	_ "github.com/tailscale/aperture-cli/internal/clients/omp"
	_ "github.com/tailscale/aperture-cli/internal/clients/opencode"
	_ "github.com/tailscale/aperture-cli/internal/clients/pi"
)

var (
	flagVersion = flag.Bool("version", false, "print version and exit")
	flagDebug   = flag.Bool("debug", false, "enable bridge diagnostics and print agent launch environment")

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

// startRunLog points slog at the run log and returns its closer. Records are
// written straight through, so the os.Exit paths that skip the close lose
// nothing; the close is there to be tidy, not to flush.
//
// A run that cannot open the file still runs: diagnostics are not worth
// refusing to start over. It falls back to discarding them rather than to
// stderr, because stderr is the TUI's screen.
//
// verbose only raises the level. The log is on for every run: the run worth
// reading back is the one that went wrong, and nobody knows to pass -debug
// before it does.
func startRunLog(verbose bool) func() {
	f, err := config.OpenRunLog()
	if err != nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return func() {}
	}
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})))
	slog.Info("aperture starting", "version", buildVersion, "commit", buildCommit, "pid", os.Getpid())
	return func() {
		slog.Info("aperture exiting")
		f.Close()
	}
}

// reportFailure puts a failure back in front of the user. Every diagnostic now
// goes to the run log, which is the right place for a running TUI and the
// wrong one for a run that just died: without this, a launch that fails prints
// nothing and exits 1.
//
// stderr is safe at both call sites: the TUI either never started or has
// already given the terminal back.
func reportFailure(err error) {
	fmt.Fprintln(os.Stderr, "aperture:", err)
	if path, pathErr := config.RunLogPath(); pathErr == nil {
		fmt.Fprintln(os.Stderr, "details:", path)
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

	// Before anything that logs. slog's default handler writes to stderr,
	// which on a TUI that owns the terminal means a line painted over the
	// screen, so until this runs every diagnostic is either damage or lost.
	closeLog := startRunLog(*flagDebug)
	defer closeLog()

	g, err := config.Load()
	if err != nil {
		slog.Error("loading launcher config", "err", err)
		reportFailure(err)
		os.Exit(1)
	}
	g.Debug = *flagDebug

	// Register Claude Desktop on supported platforms (darwin, windows).
	profiles.RegisterIfSupported()

	bridgeManager := bridges.NewManager(g.Debug)
	p := tea.NewProgram(tui.NewModel(g, buildVersion, bridgeManager))

	var exitCode int
	if _, err := p.Run(); err != nil {
		slog.Error("launcher error", "err", err)
		reportFailure(err)
		exitCode = 1
	}
	if err := bridgeManager.Close(); err != nil {
		slog.Error("shutting down bridges", "err", err)
		reportFailure(err)
		exitCode = 1
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
