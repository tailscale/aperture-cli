package tui

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/aymanbagabas/go-osc52/v2"
)

// openURL asks the desktop to open a link. Start, not Run: the opener can block
// for as long as the browser it launches lives, and a headless box fails here
// by having no opener, which Start already reports. Overridable in tests.
var openURL = func(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	// Anything the opener prints would land in the middle of the TUI.
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap it; the opener outlives this call
	return nil
}

// copyToClipboard puts s on the clipboard of whatever terminal is displaying
// this TUI, over OSC 52. A local helper (xclip, pbcopy) writes to the clipboard
// of the host aperture runs on, which over SSH is the wrong computer and the
// case where the user most needs the link. Overridable in tests.
//
// Terminals without OSC 52, or with it off, drop the sequence silently, so a
// nil error means sent, not pasted.
var copyToClipboard = func(s string) error {
	seq := osc52.New(s)
	// tmux and screen eat escape sequences they don't recognize, so the
	// passthrough wrapping is what gets this to the outer terminal. tmux sets
	// TERM to a screen-* value of its own, so it has to be checked first.
	switch {
	case os.Getenv("TMUX") != "":
		seq = seq.Tmux()
	case strings.HasPrefix(os.Getenv("TERM"), "screen"):
		seq = seq.Screen()
	}
	_, err := seq.WriteTo(os.Stdout)
	return err
}
