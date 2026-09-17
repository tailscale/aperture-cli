// Package connection carries what a connection attempt reports while it runs.
//
// It exists so the producer of these events does not own their vocabulary.
// Most of them come from the bridge manager, but the attempt is wider than the
// bridge: the model fetch that follows bring-up is part of the same wait, and
// the user does not know or care which half they are in. The types therefore
// sit below both.
//
// Nothing here may reference tsnet, ipn or ipnstate. Translating the tailnet's
// vocabulary into this one is the bridge manager's job, and this package is
// what it translates into.
package connection

import (
	"fmt"
	"net/url"
	"strings"
)

// Phase is what an attempt is waiting on, named for what the user is waiting
// for rather than for the backend state underneath it.
//
// AwaitingLoginLink and AwaitingAuthorization are the reason this type exists.
// Both are ipn.NeedsLogin, and they are completely different problems: one is
// the control plane not having answered yet, the other is the user not having
// finished in the browser. A bridge that took 29 seconds to come up spent them
// in the first and showed only "NeedsLogin", so there was nothing on screen to
// tell the two apart and three fixes were aimed at the wrong one.
type Phase int

// Phases in the order an attempt passes through them. The order is load
// bearing: a phase only ever moves forward, and Entered compares them.
const (
	StartingMachine Phase = iota
	AwaitingLoginLink
	AwaitingAuthorization
	JoiningTailnet
	FindingEndpoint
	AskingForModels
)

// String is what the connect screen shows, so it names the wait from the
// user's side. The attempt's elapsed clock supplies the "how long".
func (p Phase) String() string {
	switch p {
	case StartingMachine:
		return "Starting the bridge"
	case AwaitingLoginLink:
		return "Waiting for a login link"
	case AwaitingAuthorization:
		return "Waiting for you to authorize this bridge"
	case JoiningTailnet:
		return "Joining the tailnet"
	case FindingEndpoint:
		return "Looking for the Aperture on the tailnet"
	case AskingForModels:
		return "Asking the Aperture for its models"
	}
	return fmt.Sprintf("Phase(%d)", int(p))
}

// LoginLink is the URL that authorizes a machine on a tailnet.
type LoginLink struct {
	url string
}

func (l LoginLink) String() string { return l.url }

// ParseLoginLink validates a login link and is the only way to make one.
//
// The rules are not cosmetic: the value is handed to a desktop opener and
// shown as something the user should click, so anything that is not an https
// URL is not a link we were asked to follow. Tailscale applies the same rules
// upstream in validPopBrowserURLLocked; this is the second gate, not the first.
func ParseLoginLink(raw string) (LoginLink, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return LoginLink{}, fmt.Errorf("login link is empty")
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return LoginLink{}, fmt.Errorf("login link contains whitespace: %q", raw)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return LoginLink{}, fmt.Errorf("login link is not a URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return LoginLink{}, fmt.Errorf("login link is not https: %q", raw)
	}
	if parsed.Host == "" {
		return LoginLink{}, fmt.Errorf("login link has no host: %q", raw)
	}
	return LoginLink{url: raw}, nil
}

// Kind distinguishes the events an attempt publishes.
type Kind int

const (
	// Noted is diagnostics with no domain meaning: tsnet backend chatter, dial
	// detail, a health warning. The only kind a consumer may drop.
	Noted Kind = iota
	// PhaseEntered is the attempt moving to a new wait.
	PhaseEntered
	// LoginRequired is a machine asking to be authorized at a link.
	LoginRequired
)

// Event is what an attempt publishes as it proceeds. It replaces a log sink of
// plain strings, which forced every consumer to recover meaning by matching
// prose: the TUI opened a browser on a phrase from inside a vendored package,
// so a reworded upstream log line would silently strand the user.
type Event struct {
	Kind  Kind
	Phase Phase     // Kind == PhaseEntered
	Link  LoginLink // Kind == LoginRequired
	Text  string    // Kind == Noted
}

// Note reports diagnostics, flattened to one line.
//
// Flattened here rather than at each consumer because String promises one line
// of the activation log and the screen relies on it: the connect screen wraps
// and indents each line itself, and an embedded newline puts unindented text
// in the middle of the block and miscounts the rows the renderer has to
// repaint. Control plane errors arrive with the request ID on a second line,
// so this is the normal shape of a failure, not a malformed one.
func Note(text string) Event {
	return Event{Kind: Noted, Text: strings.Join(strings.Fields(text), " ")}
}

// Notef reports diagnostics, formatted.
func Notef(format string, args ...any) Event { return Note(fmt.Sprintf(format, args...)) }

// Entered reports that the attempt is now waiting on p.
func Entered(p Phase) Event { return Event{Kind: PhaseEntered, Phase: p} }

// Login reports that the machine needs authorizing at link.
func Login(link LoginLink) Event { return Event{Kind: LoginRequired, Link: link} }

// Droppable reports whether a consumer under backpressure may discard this
// event. Only diagnostics may go: losing a phase leaves a gap in the record of
// where the time went, and losing a login link leaves the user waiting on a
// browser tab that was never opened at a URL they were never shown. The old
// sink dropped whatever arrived on a full buffer, and under -debug the tsnet
// backend logger shared that buffer, so a burst of chatter could take the one
// line the user could not proceed without.
func (e Event) Droppable() bool { return e.Kind == Noted }

// String renders the event as one line of the activation log.
func (e Event) String() string {
	switch e.Kind {
	case PhaseEntered:
		return e.Phase.String()
	case LoginRequired:
		return "Authorize this bridge at " + e.Link.String()
	default:
		return e.Text
	}
}
