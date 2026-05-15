// Package menu defines the descriptors the TUI uses to render a generic,
// navigable menu stack. Every client package builds its install/launch flow
// by returning nested Menu values from its action closures; the TUI takes
// care of cursor movement, digit shortcuts, Esc-to-pop, and dispatching
// tea.Cmds.
package menu

import (
	tea "github.com/charmbracelet/bubbletea"
)

// DigitZero pins a MenuItem to the numeric shortcut "0". It is a distinct
// sentinel because zero is the natural zero value for the Digit field,
// which has "use default" semantics.
const DigitZero = -10

// Kind identifies a MenuItem's rendering style.
type Kind int

const (
	// KindDefault is a normal selectable row.
	KindDefault Kind = iota
	// KindInput is a single-line text input row; the TUI captures keystrokes
	// into MenuItem.Label and calls Action on Enter.
	KindInput
)

// MenuItem is one selectable row in a Menu.
type MenuItem struct {
	Label       string
	Description string
	Kind        Kind
	Disabled    bool
	// Hidden items are skipped by the renderer but kept in the slice so
	// numeric shortcuts remain stable.
	Hidden bool
	// Digit, when set to a non-negative value, renders as the leading "[N]"
	// prefix and makes the item selectable via that keystroke. A zero
	// value means "use the default": the item's 1-based position in the
	// menu's Items slice. Set Digit to DigitZero to pin the item to [0].
	Digit int
	// Shortcut is an alternative single-character key that activates this
	// item (e.g. "s" for Settings, "a" for Add endpoint). Empty disables.
	Shortcut string
	// Action runs when the item is selected.
	Action func() Result
}

// Menu is a list of selectable items plus optional title and footer hint.
type Menu struct {
	Title string
	// Preamble is optional static text rendered (dimmed) between the title
	// and the item list. Use it for informational paragraphs that are not
	// selectable.
	Preamble string
	Items    []MenuItem
	Hint     string
	// OnBack, when non-nil, overrides the default "pop stack one level"
	// behavior on Esc. Returning a nil tea.Cmd simply stays on this menu.
	OnBack func() tea.Cmd
}

// Result is what an Action returns. Exactly one field is populated.
type Result struct {
	// Next pushes a submenu onto the stack.
	Next *Menu
	// Replace swaps the top of the stack in place.
	Replace *Menu
	// Cmd dispatches a tea.Cmd. The engine pops the stack when the command's
	// done-msg arrives (typically via ExecProcess). Use PopOnDone=false to
	// suppress that.
	Cmd       tea.Cmd
	PopOnDone bool
	// Pop goes back one level.
	Pop bool
	// Quit terminates the program.
	Quit bool
}
