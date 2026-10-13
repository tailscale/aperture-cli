package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/bridges"
	"github.com/tailscale/aperture-cli/internal/cli"
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
		if desc := gitDescribe(); desc != "" {
			buildVersion = desc
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

// gitDescribe reports the release version of the checkout containing this
// source file: the tag on an exact release commit, or the nearest tag with
// the distance and commit appended. It returns "" outside a checkout.
func gitDescribe() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return gitDescribeInDir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
	}
}

func gitDescribeInDir(dir string) string {
	cmd := exec.Command("git", "describe", "--tags", "--always", "--dirty")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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

// versionLine reports the build version with its commit and date when known.
func versionLine() string {
	if buildCommit != "unknown" {
		return fmt.Sprintf("%s (%s, %s)", buildVersion, buildCommit, buildDate)
	}
	return buildVersion
}

// runTUI starts the interactive launcher, the command a bare invocation
// gets. endpoint, bridge and debug come from the command line resolved.
func runTUI(endpoint, bridge string, debug bool) error {
	if debug {
		config.RaiseRunLog()
	}
	g, err := config.Load()
	if err != nil {
		slog.Error("loading launcher config", "err", err)
		return err
	}
	g.Debug = debug

	// Register Claude Desktop on supported platforms (darwin, windows).
	profiles.RegisterIfSupported()

	// Resolve the flags before the TUI takes the terminal. A URL we cannot use
	// then exits non-zero instead of painting an error the script that passed
	// it will never see.
	start, err := config.EndpointFromFlags(g, endpoint, bridge)
	if err != nil {
		slog.Error("resolving the endpoint to open on", "err", err)
		return err
	}

	machines := bridges.NewMachines(g.Debug)
	p := tea.NewProgram(tui.NewModel(g, buildVersion, machines, start))

	var runErr error
	if _, err := p.Run(); err != nil {
		slog.Error("launcher error", "err", err)
		runErr = err
	}
	if err := machines.Close(); err != nil {
		slog.Error("shutting down bridges", "err", err)
		if runErr == nil {
			runErr = err
		}
	}
	return runErr
}

func main() {
	// The log opens before dispatch, so every run logs, headless ones too.
	// The TUI still owns the terminal: nothing else writes to stderr here.
	closeLog := config.StartRunLog()
	defer closeLog()
	slog.Info("aperture starting", "version", buildVersion, "commit", buildCommit, "pid", os.Getpid())
	if err := cli.Execute(os.Args[1:], versionLine(), runTUI); err != nil {
		// A launched client's exit code is aperture's own: an alias for the
		// client has to behave like the client. Its stderr already said why.
		// A signaled child reports -1, which would wrap to 255.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if code := exitErr.ExitCode(); code > 0 {
				os.Exit(code)
			}
			os.Exit(1)
		}
		reportFailure(err)
		os.Exit(1)
	}
}
