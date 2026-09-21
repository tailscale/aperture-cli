package bridges

import (
	"log/slog"
	"net/url"
	"regexp"
	"sync"

	"github.com/tailscale/aperture-cli/internal/connection"
)

// liveEvents points a node's long-lived reporting at whichever connection is
// using it now. Nodes and proxies outlive the connection that built them, and
// closures that captured that connection's sink went on writing to a channel
// nobody read, losing every later dial failure and proxy error.
//
// Nothing clears it when a connection ends: a finished sink discards what it is
// given, and a clear needs a lifecycle hook only the Attempt can own.
type liveEvents struct {
	mu sync.Mutex
	ev events
}

func (l *liveEvents) use(ev events) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ev = ev
}

// emit has the events signature, so callers keep note and notef.
func (l *liveEvents) emit(e connection.Event) {
	l.mu.Lock()
	ev := l.ev
	l.mu.Unlock()
	if ev != nil {
		ev(e)
	}
}

// events is where a bridge reports what it is doing. This package translates
// the tailnet's vocabulary into it and publishes nothing else, so no caller has
// to recover meaning by matching prose from inside a vendored package.
type events func(connection.Event)

// sink returns a usable events, so callers that want none can pass nil.
func sink(emit func(connection.Event)) events {
	return func(e connection.Event) {
		logEvent(e)
		if emit != nil {
			emit(e)
		}
	}
}

// logEvent copies a connection event into the run log. The connect screen dies
// with the process, and the run anyone wants to read back is the one that was
// killed halfway through. Notes are debug: under -debug they carry tsnet's
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

// Backend diagnostics can repeat authorization capabilities. Keep the link in
// the interactive event only; even debug logs are routinely shared for support.
var diagnosticURL = regexp.MustCompile(`(?i)https?://\S+`)

func redactDiagnostic(text string) string {
	return diagnosticURL.ReplaceAllString(text, "[redacted URL]")
}

func (e events) note(text string)                        { e(connection.Note(text)) }
func (e events) notef(format string, args ...any)        { e(connection.Notef(format, args...)) }
func (e events) enter(p connection.Phase)                { e(connection.Entered(p)) }
func (e events) loginRequired(link connection.LoginLink) { e(connection.LoginRequired(link)) }

// redactURL is the part of an endpoint URL safe for the run log: scheme and
// host. ParseEndpointURL accepts userinfo and a query, and a run log is the
// file people share when asking for help.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[redacted URL]"
	}
	return u.Scheme + "://" + u.Host
}
