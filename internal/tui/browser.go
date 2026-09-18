package tui

import (
	"os"
	"strings"

	"github.com/aymanbagabas/go-osc52/v2"
)

// openURL asks the desktop to open a link. Overridable in tests.
var openURL = platformOpenURL

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
