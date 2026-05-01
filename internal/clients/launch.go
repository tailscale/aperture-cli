package clients

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tailscale/aperture-cli/internal/menu"
)

// LaunchSpec describes a foreground client-binary launch. The TUI hands
// control to the child process and regains it when the child exits.
type LaunchSpec struct {
	// Binary is the absolute path to the executable. Required.
	Binary string
	// Args are appended to the command line after Binary.
	Args []string
	// Env is overlaid on top of os.Environ(). Later keys override earlier
	// ones; within Env, order is unspecified (map).
	Env map[string]string
	// Cleanup runs after the child exits, before the done-msg is emitted.
	// Use it to remove temporary config files.
	Cleanup func()
	// Debug, when true, dumps the resolved Env and Args to stderr before
	// exec (matches the `-debug` flag wiring).
	Debug bool
}

// Launch returns a tea.Cmd that runs the given spec via tea.ExecProcess and
// emits menu.ExecDoneMsg when the child exits. If spec.Binary is empty,
// the command returns immediately with an error.
func Launch(spec LaunchSpec) tea.Cmd {
	if spec.Binary == "" {
		err := fmt.Errorf("binary path is empty")
		return func() tea.Msg { return menu.ExecDoneMsg{Err: err} }
	}

	envPairs := os.Environ()
	for k, v := range spec.Env {
		envPairs = append(envPairs, k+"="+v)
	}

	if spec.Debug {
		fmt.Fprintf(os.Stderr, "\r\n[debug] launching %s\r\n", spec.Binary)
		for k, v := range spec.Env {
			fmt.Fprintf(os.Stderr, "[debug]   %s=%s\r\n", k, v)
		}
		if len(spec.Args) > 0 {
			fmt.Fprintf(os.Stderr, "[debug]   args: %v\r\n", spec.Args)
		}
	}

	cmd := exec.Command(spec.Binary, spec.Args...)
	cmd.Env = envPairs
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if spec.Cleanup != nil {
			spec.Cleanup()
		}
		return menu.ExecDoneMsg{Err: err}
	})
}
