package copilot

import (
	"os"
	"path/filepath"
)

func commonBinaryPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin", "copilot"),
		filepath.Join(home, ".npm-global", "bin", "copilot"),
	}
}
