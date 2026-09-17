package tui

import (
	"os/exec"
	"runtime"
	"strings"

	"github.com/tailscale/aperture-cli/internal/bridges"
)

// tsnetAuthURLMarker is what tsnet logs ahead of the login link while a bridge
// waits to be authorized ("... restart with TS_AUTHKEY set, or go to: <url>").
// It repeats the whole line every few seconds until login completes.
const tsnetAuthURLMarker = "or go to: "

// authURLMarkers are the two phrasings that carry a login link. The bridge
// manager emits its line the moment the IPN bus has the link; tsnet's own
// line comes out of a five second poll, so it usually repeats one that is
// already on screen.
var authURLMarkers = []string{bridges.AuthLogPrefix, tsnetAuthURLMarker}

// authURLFromLog returns the Tailscale login link a bridge log line carries,
// or "" when it carries none. The https:// requirement is not cosmetic: the
// result is handed to a desktop opener, and anything else (a file path, a
// leading dash) is not a link the user asked us to follow.
func authURLFromLog(line string) string {
	for _, marker := range authURLMarkers {
		_, rest, ok := strings.Cut(line, marker)
		if !ok {
			continue
		}
		url := strings.TrimSpace(rest)
		if !strings.HasPrefix(url, "https://") || strings.ContainsAny(url, " \t") {
			return ""
		}
		return url
	}
	return ""
}

// openURL asks the desktop to open a link. Start, not Run: the opener can
// block for as long as the browser it launches lives, and a headless box
// fails here by not having an opener at all, which Start already reports.
func openURL(url string) error {
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
