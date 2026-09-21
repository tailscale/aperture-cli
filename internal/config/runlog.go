package config

import (
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
