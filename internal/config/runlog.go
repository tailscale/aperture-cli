package config

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// runLogCap bounds the run log. The next run starts the file over once it
// passes this size. A connect attempt writes a few hundred bytes, so the cap
// holds a long history and still cannot grow without bound on a box nobody
// prunes.
//
// The file is truncated, not rotated. Rotate if anyone ever needs the older
// runs.
const runLogCap = 2 << 20

// RunLogPath returns the file every run writes its diagnostics to. The file
// sits beside the settings and bridge state rather than in a temp dir. The
// question it answers ("what was the last run waiting on?") gets asked after
// a reboot as often as before one.
func RunLogPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aperture", "aperture.log"), nil
}

// OpenRunLog opens the run log for appending, creating the directory on first
// use and starting the file over once it passes runLogCap.
func OpenRunLog() (*os.File, error) {
	path, err := RunLogPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(path); err == nil && info.Size() > runLogCap {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0o600)
}

// runLog is the open run log of this process, nil when the log could not
// open. RaiseRunLog needs the file after StartRunLog chose it.
var runLog *os.File

// StartRunLog points slog at the run log for the whole process and returns
// the close. Records are written straight through, so the os.Exit paths
// that skip the close lose nothing. The close is tidiness, not a flush.
//
// A run that cannot open the file still runs. Diagnostics are not worth
// refusing to start over. Such a run discards them rather than writing to
// stderr, because stderr is the TUI's screen.
//
// The log is on for every run, not only the interactive one. The run worth
// reading back is the one that went wrong, and nobody knows to pass -debug
// before it does.
func StartRunLog() func() {
	f, err := OpenRunLog()
	if err != nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return func() {}
	}
	runLog = f
	slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
	return func() {
		slog.Info("aperture exiting")
		f.Close()
		runLog = nil
	}
}

// RaiseRunLog raises the run log to debug level. Call it from the path that
// learned the --debug flag after dispatch; the log itself opens before any
// command runs, so every run logs either way.
func RaiseRunLog() {
	if runLog == nil {
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(runLog, &slog.HandlerOptions{Level: slog.LevelDebug})))
}
