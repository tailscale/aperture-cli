// Package clients holds the registry of AI coding agent clients (Claude Code,
// Codex, Gemini, OpenCode, ...). Each client owns its own install, launch,
// and configuration logic inside a sub-package; this file provides shared
// helpers for discovering client binaries on disk.
package clients

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// FindBinary returns the resolved path to a client binary. It checks
// exec.LookPath (i.e. $PATH) first, then the client-supplied extra paths,
// then general well-known user-local binary directories. Returns "" if the
// binary cannot be found.
func FindBinary(name string, extraPaths []string) string {
	if name == "" {
		return ""
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, p := range extraPaths {
		if isExecutable(p) {
			return p
		}
	}
	for _, dir := range commonBinDirs() {
		p := filepath.Join(dir, name)
		if isExecutable(p) {
			return p
		}
	}
	return ""
}

// IsInstalled reports whether the named binary can be found on disk.
func IsInstalled(name string, extraPaths []string) bool {
	if name == "" {
		return true
	}
	return FindBinary(name, extraPaths) != ""
}

// commonBinDirs returns well-known user-local directories that may not be on
// PATH yet (e.g. after a fresh install that updated shell profiles but the
// running shell still has the old PATH). System-wide directories are
// intentionally excluded: binaries there are found by exec.LookPath.
func commonBinDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
		filepath.Join(home, ".npm-global", "bin"),
	}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(path))
		return ext == ".exe" || ext == ".cmd" || ext == ".bat" || ext == ".com"
	}
	return info.Mode()&0o111 != 0
}
