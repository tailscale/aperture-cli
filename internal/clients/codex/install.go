package codex

import (
	"os"
	"path/filepath"
)

// commonBinaryPaths returns the non-PATH locations where `codex` is
// commonly installed.
func commonBinaryPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin", "codex"),
	}
}
