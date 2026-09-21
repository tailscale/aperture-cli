package bridges

import (
	"log/slog"
	"net/url"
	"regexp"
	"sync"

	"github.com/tailscale/aperture-cli/internal/connection"
)

// eventRelay forwards a Machine's events to the attempt using the Machine
// now. The node and its proxies outlive the attempt that built them, and a
// closure that captured the first attempt's sink kept writing to a channel
// nobody read, losing every later dial failure and proxy error.
//
// Nothing clears the relay when an attempt ends. A finished sink discards what
// it is given, and clearing would need a lifecycle hook only the Attempt
// could own.
type eventRelay struct {
	mu sync.Mutex
	to events
}

// forwardTo makes ev the current recipient.
func (r *eventRelay) forwardTo(ev events) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.to = ev
}

// emit has the events signature, so events(r.emit) gives callers note and
// notef.
func (r *eventRelay) emit(e connection.Event) {
	r.mu.Lock()
	ev := r.to
	r.mu.Unlock()
	if ev != nil {
		ev(e)
	}
}

// events receives what a bridge reports: the phase it entered, the login
// link it needs visited, or a note for the user. This package translates
// tsnet's vocabulary into connection.Event and publishes nothing else, so no
// caller has to match log lines from a vendored package.
type events func(connection.Event)

// sink wraps emit, which may be nil, as an events that also writes to the
// run log.
func sink(emit func(connection.Event)) events {
	return func(e connection.Event) {
		logEvent(e)
		if emit != nil {
			emit(e)
		}
	}
}

// logEvent writes an event to the run log. The connect screen dies with the
// process, and the run anyone wants to read back is the one that was killed
// halfway through. Notes log at debug: under -debug they carry tsnet's
// backend logger, and a phase is worth reading without wading through that.
func logEvent(e connection.Event) {
	switch {
	case e.Phase != 0:
		slog.Info("bridge phase", "phase", e.Phase)
	case e.Link != nil:
		slog.Info("bridge needs login")
	default:
		slog.Debug("bridge note", "text", redactDiagnostic(e.Note))
	}
}

// Backend diagnostics can repeat the login link, which authorizes a device.
// The link stays in the interactive event only: even debug logs get shared
// for support.
var diagnosticURL = regexp.MustCompile(`(?i)https?://\S+`)

func redactDiagnostic(text string) string {
	return diagnosticURL.ReplaceAllString(text, "[redacted URL]")
}

func (e events) note(text string)                        { e(connection.Note(text)) }
func (e events) notef(format string, args ...any)        { e(connection.Notef(format, args...)) }
func (e events) enter(p connection.Phase)                { e(connection.Entered(p)) }
func (e events) loginRequired(link connection.LoginLink) { e(connection.LoginRequired(link)) }

// redactURL keeps only the scheme and host. ParseEndpointURL accepts userinfo
// and a query, and the run log is the file people share when asking for
// help.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[redacted URL]"
	}
	return u.Scheme + "://" + u.Host
}
