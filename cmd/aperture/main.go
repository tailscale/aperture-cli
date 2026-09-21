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

	// Each import registers its client with internal/clients.
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
	flagVersion  = flag.Bool("version", false, "print version and exit")
	flagDebug    = flag.Bool("debug", false, "enable bridge diagnostics and print agent launch environment")
	flagEndpoint = flag.String("endpoint", "", "Aperture URL to open on, instead of the saved one ($APERTURE_ENDPOINT)")
	flagBridge   = flag.String("bridge", "", "connect through the bridge with this name, creating it if there is none ($APERTURE_BRIDGE)")

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

	// ldflags may have set these already. Fill them from VCS info only then.
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

// startRunLog points slog at the run log and returns a function that closes
// it. Records are written straight through, so the os.Exit paths that skip
// the close lose nothing. The close is tidiness, not a flush.
//
// A run that cannot open the file still runs. Diagnostics are not worth
// refusing to start over. Such a run discards them rather than writing to
// stderr, because stderr is the TUI's screen.
//
// verbose only raises the level. The log is on for every run. The run worth
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

// reportFailure prints err and the run log path to stderr. Every diagnostic
// goes to the run log, which is right for a running TUI and wrong for a run
// that just died. Without this, a failed launch prints nothing and exits 1.
//
// stderr is safe at both call sites. The TUI either never started or has
// already given the terminal back.
func reportFailure(err error) {
	fmt.Fprintln(os.Stderr, "aperture:", err)
	if path, pathErr := config.RunLogPath(); pathErr == nil {
		fmt.Fprintln(os.Stderr, "details:", path)
	}
}

// flagOrEnv returns flag name when it was passed, even empty, and otherwise
// the environment variable key. The variable lets a dotfile, container or
// systemd unit make the same selection a typed invocation can. The flag wins
// so a one-off run can override the shell it started in: -bridge= means no
// bridge.
func flagOrEnv(fs *flag.FlagSet, name, key string) string {
	passed := false
	fs.Visit(func(f *flag.Flag) { passed = passed || f.Name == name })
	if passed {
		return fs.Lookup(name).Value.String()
	}
	return os.Getenv(key)
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

	// Start the log before anything that logs. slog's default handler writes
	// to stderr, and on a TUI that owns the terminal that paints a line over
	// the screen. Until this runs every diagnostic is either damage or lost.
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

	// Resolve the flags before the TUI takes the terminal. A URL we cannot use
	// then exits non-zero instead of painting an error the script that passed
	// it will never see.
	start, err := config.EndpointFromFlags(g, flagOrEnv(flag.CommandLine, "endpoint", "APERTURE_ENDPOINT"), flagOrEnv(flag.CommandLine, "bridge", "APERTURE_BRIDGE"))
	if err != nil {
		slog.Error("resolving the endpoint to open on", "err", err)
		reportFailure(err)
		os.Exit(1)
	}

	machines := bridges.NewMachines(g.Debug)
	p := tea.NewProgram(tui.NewModel(g, buildVersion, machines, start))

	var exitCode int
	if _, err := p.Run(); err != nil {
		slog.Error("launcher error", "err", err)
		reportFailure(err)
		exitCode = 1
	}
	if err := machines.Close(); err != nil {
		slog.Error("shutting down bridges", "err", err)
		reportFailure(err)
		exitCode = 1
	}
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
