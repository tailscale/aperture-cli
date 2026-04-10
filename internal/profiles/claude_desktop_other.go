//go:build !darwin && !windows

package profiles

import (
	"fmt"
	"os/exec"
)

// On unsupported platforms, return a binary name that won't be found so the
// profile doesn't appear as installed in the TUI.
func platformBinaryName() string { return "claude-desktop-not-available" }

func platformCommonPaths() []string { return nil }

func platformInstallHint() string { return "" }

func platformConfigure(_ string) error {
	return fmt.Errorf("Claude Cowork configuration is only supported on macOS and Windows")
}

func platformReadGatewayURL() string { return "" }

func platformInstallCmd() *exec.Cmd {
	return exec.Command("echo", "Claude Cowork is only supported on macOS and Windows")
}

func platformLaunch() error {
	return fmt.Errorf("Claude Cowork is only supported on macOS and Windows")
}
