package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tailscale/aperture-cli/internal/config"
)

// scratchHome points the config directory at a scratch home for the whole
// test, so several invocations share one settings file. It also clears the
// process-wide settings path: UseSettingsFile survives the test that set
// it, and a repeated run (-count=2) fails on stale state without this.
func scratchHome(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	config.UseSettingsFile("")
}

// runCLI executes the command tree once and returns what it printed.
// runTUI must never run: these tests cover commands.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := rootCommand("test-version", func(_, _ string, _ bool) error {
		t.Fatal("a command invocation started the TUI")
		return nil
	})
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// The contract makes help output part of the surface: every command shows
// example invocations, so a missing Example is a broken contract, not a
// style miss.
func TestEveryCommandDocumentsExamples(t *testing.T) {
	root := rootCommand("test-version", func(_, _ string, _ bool) error { return nil })
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Runnable() && cmd.Example == "" {
			t.Errorf("%s has no examples in its help", cmd.CommandPath())
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
	for _, path := range []string{"aperture", "aperture bridge", "aperture endpoint"} {
		cmd, _, err := root.Find(strings.Split(path, " ")[1:])
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if cmd.Example == "" {
			t.Errorf("%s has no examples in its help", path)
		}
	}
}

// --config selects the settings file for the command, which is what lets two
// apertures or a test keep separate configuration.
func TestConfigFlagSelectsTheSettingsFile(t *testing.T) {
	scratchHome(t)
	settings := filepath.Join(t.TempDir(), "elsewhere.json")
	if _, _, err := runCLI(t, "--config", settings, "bridge", "add", "work"); err != nil {
		t.Fatalf("bridge add: %v", err)
	}
	stdout, _, err := runCLI(t, "--config", settings, "bridge", "list")
	if err != nil {
		t.Fatalf("bridge list: %v", err)
	}
	if !strings.Contains(stdout, "work") {
		t.Errorf("bridge list = %q, want the bridge the other invocation added", stdout)
	}
}
