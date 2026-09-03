package hermes

import (
	"os"
	"path/filepath"
)

// commonBinaryPaths returns the non-PATH locations where hermes is commonly installed.
func commonBinaryPaths() []string {
	paths := []string{
		filepath.Join("/usr", "local", "bin", binaryName),
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return paths
	}
	return append(paths, filepath.Join(home, ".local", "bin", binaryName))
}
