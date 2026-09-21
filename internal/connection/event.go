// Package connection defines what a connection attempt reports while it runs:
// the phase it is waiting in, the login link it needs visited, and the odd
// line for the user that is neither. A Machine produces most of these, but the
// attempt is wider than the Machine, since the model fetch after bring-up is
// part of the same wait. So the vocabulary lives here rather than with the
// tailnet code.
//
// Nothing here may reference tsnet, ipn or ipnstate. internal/bridges
// translates the tailnet's vocabulary into this one.
package connection

import (
	"fmt"
	"net/url"
	"strings"
)

// Phase names what an attempt is waiting on. Phases are named for what the
// user is waiting for, not for the backend state underneath. The zero value
// is no phase.
//
// AwaitingLoginLink and AwaitingAuthorization are why this type exists. Both
// are ipn.NeedsLogin and they are different problems: the control plane has
// not answered yet, versus the user has not finished in the browser. A 29
// second bridge spent them in the first and showed only "NeedsLogin".
type Phase int

// Phases in the order an attempt passes through them. The order matters: a
// phase only ever moves forward, and consumers compare them.
const (
	StartingMachine Phase = iota + 1
	AwaitingLoginLink
	AwaitingAuthorization
	JoiningTailnet
	FindingEndpoint
	AskingForModels
)

var phaseNames = [...]string{
	StartingMachine:       "StartingMachine",
	AwaitingLoginLink:     "AwaitingLoginLink",
	AwaitingAuthorization: "AwaitingAuthorization",
	JoiningTailnet:        "JoiningTailnet",
	FindingEndpoint:       "FindingEndpoint",
	AskingForModels:       "AskingForModels",
}

// String returns the phase's name, for logs and errors. The presentation
// layer decides what the user reads for it.
func (p Phase) String() string {
	if p > 0 && int(p) < len(phaseNames) {
		return phaseNames[p]
	}
	return fmt.Sprintf("Phase(%d)", int(p))
}

// LoginLink is the URL the user visits to authorize a machine on a tailnet.
type LoginLink struct {
	url string
}

func (l LoginLink) String() string { return l.url }

// ParseLoginLink validates raw and is the only way to make a LoginLink. The
// value is handed to a desktop opener and shown as something to click, so
// anything that is not an https URL is rejected. Tailscale applies the same
// rules in validPopBrowserURLLocked.
func ParseLoginLink(raw string) (LoginLink, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return LoginLink{}, fmt.Errorf("login link is empty")
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return LoginLink{}, fmt.Errorf("login link contains whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return LoginLink{}, fmt.Errorf("login link is not a URL")
	}
	if parsed.Scheme != "https" {
		return LoginLink{}, fmt.Errorf("login link is not https")
	}
	if parsed.Host == "" {
		return LoginLink{}, fmt.Errorf("login link has no host")
	}
	return LoginLink{url: raw}, nil
}

// Event is one report from a running connection attempt. Exactly one field
// is set. Phase means the attempt entered that phase. Link means the Machine
// needs authorizing at that URL. Note is a line for the user with no domain
// meaning.
//
// Event replaced a sink of plain strings. The TUI used to open a browser on a
// phrase matched from inside a vendored package, and a reworded log line
// silently stranded the user.
type Event struct {
	Phase Phase
	Link  *LoginLink
	Note  string
}

// Note returns an Event carrying a line for the user that is neither a phase
// nor a link: tsnet backend chatter, dial detail, a health warning. A Note is
// the only event a consumer may drop.
func Note(text string) Event { return Event{Note: text} }

// Notef returns Note with the text formatted.
func Notef(format string, args ...any) Event { return Note(fmt.Sprintf(format, args...)) }

// Entered returns the Event for an attempt entering p.
func Entered(p Phase) Event { return Event{Phase: p} }

// LoginRequired returns the Event for a Machine that needs authorizing at
// link.
func LoginRequired(link LoginLink) Event { return Event{Link: &link} }

// Droppable reports whether a consumer under backpressure may discard this
// event. Only a Note may be dropped. A lost phase leaves a gap in where the
// time went, and a lost login link leaves the user waiting on a browser tab
// nothing opened. The old sink dropped whatever arrived on a full buffer,
// which under -debug it shared with tsnet's backend logger.
func (e Event) Droppable() bool { return e.Phase == 0 && e.Link == nil }

// String returns the event for logs and test failures: the phase's name, the
// link marked as one, or the note's text.
func (e Event) String() string {
	switch {
	case e.Phase != 0:
		return e.Phase.String()
	case e.Link != nil:
		return "LoginRequired(" + e.Link.String() + ")"
	}
	return e.Note
}
