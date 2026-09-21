package tui

import (
	"os"
	"strings"

	"github.com/aymanbagabas/go-osc52/v2"
)

// openURL asks the desktop to open a link. Overridable in tests.
var openURL = platformOpenURL

// copyToClipboard puts s on the clipboard of the terminal displaying this
// TUI, over OSC 52. A local helper such as xclip or pbcopy would write to the
// clipboard of the host aperture runs on. Over SSH that is the wrong computer,
// and SSH is where the user most needs the link. Tests override it.
//
// Terminals without OSC 52, or with it off, drop the sequence silently, so a
// nil error means sent, not pasted.
var copyToClipboard = func(s string) error {
	seq := osc52.New(s)
	// tmux and screen eat escape sequences they don't recognize. The
	// passthrough wrapping carries the sequence to the outer terminal. tmux
	// sets TERM to a screen-* value of its own, so it has to be checked first.
	switch {
	case os.Getenv("TMUX") != "":
		seq = seq.Tmux()
	case strings.HasPrefix(os.Getenv("TERM"), "screen"):
		seq = seq.Screen()
	}
	_, err := seq.WriteTo(os.Stdout)
	return err
}
