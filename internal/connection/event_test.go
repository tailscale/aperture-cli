package connection

import "testing"

func TestParseLoginLink(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"a real one", "https://login.tailscale.com/a/17bceb7b0129ba", true},
		{"surrounding space is the log's, not the link's", "  https://login.tailscale.com/a/17bceb7b0129ba  ", true},
		{"plaintext", "http://evil.example.com", false},
		{"a flag, which is what a bad parse of a log line yields", "--version", false},
		{"a file path", "/etc/passwd", false},
		{"a trailing argument smuggled past the URL", "https://login.tailscale.com/a/x --flag", false},
		{"no host", "https:///a/x", false},
		{"empty", "", false},
		{"whitespace only", "   ", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			link, err := ParseLoginLink(tt.raw)
			if tt.ok != (err == nil) {
				t.Fatalf("ParseLoginLink(%q) error = %v, want ok = %t", tt.raw, err, tt.ok)
			}
			if !tt.ok {
				return
			}
			// The trim is part of the value, not of the rendering: this string
			// is handed to a desktop opener.
			if link.String() != "https://login.tailscale.com/a/17bceb7b0129ba" {
				t.Errorf("link = %q, want it trimmed", link)
			}
		})
	}
}

// TestOnlyNotesAreDroppable is the invariant the whole type exists for: the
// sink discards events when its buffer fills, and the login link sharing that
// buffer with tsnet's debug chatter is what could strand an attempt.
func TestOnlyNotesAreDroppable(t *testing.T) {
	link, err := ParseLoginLink("https://login.tailscale.com/a/x")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		event     Event
		droppable bool
	}{
		{Note("magicsock: home is derp-1"), true},
		{Notef("dialing %s", "ai"), true},
		{Entered(AwaitingLoginLink), false},
		{Login(link), false},
	} {
		if got := tt.event.Droppable(); got != tt.droppable {
			t.Errorf("%q Droppable() = %t, want %t", tt.event, got, tt.droppable)
		}
	}
}

// TestPhasesAreOrdered guards the comparison both the bus watch and the
// attempt use to reject a phase that would walk the user backwards.
func TestPhasesAreOrdered(t *testing.T) {
	ordered := []Phase{
		StartingMachine,
		AwaitingLoginLink,
		AwaitingAuthorization,
		JoiningTailnet,
		FindingEndpoint,
		AskingForModels,
	}
	for i, p := range ordered {
		if i > 0 && !(ordered[i-1] < p) {
			t.Errorf("%v does not sort before %v", ordered[i-1], p)
		}
		if p.String() == "" {
			t.Errorf("phase %d has no name for the screen", int(p))
		}
	}
}
