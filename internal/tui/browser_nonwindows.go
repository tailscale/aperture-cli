//go:build !windows

package tui

import (
	"os/exec"
	"runtime"
)

func platformOpenURL(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	cmd := exec.Command(opener, url)
	// Start, not Run: the opener can live as long as the browser. Its output
	// must not land in the TUI, so leave stdout and stderr disconnected.
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
