package menu

// ExecDoneMsg is emitted when a foreground client process exits (launched
// via tea.ExecProcess from inside a client's Action). The TUI's menu engine
// handles this by clearing the stack and re-running preflight.
type ExecDoneMsg struct{ Err error }

// InstallDoneMsg is emitted when an install command finishes. The TUI's
// menu engine handles this by rebuilding the root menu (re-scanning which
// clients are installed).
type InstallDoneMsg struct{ Err error }

// LaunchDoneMsg is emitted when a GUI launch (desktop app) returns control
// immediately. Unlike ExecDoneMsg, the TUI does not re-run preflight —
// launching a desktop app does not invalidate anything.
type LaunchDoneMsg struct{ Err error }

// SimpleDoneMsg is a generic "a tea.Cmd finished" marker that the engine
// uses to pop the stack one level. Suitable for uninstall-style actions
// that complete synchronously without touching the agent binary layout.
type SimpleDoneMsg struct{ Err error }
