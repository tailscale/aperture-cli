package omp

import (
	"os"
	"path/filepath"
)

// commonBinaryPaths returns the non-PATH locations where omp is commonly installed.
func commonBinaryPaths() []string {
	paths := []string{
		filepath.Join("/opt", "homebrew", "bin", "omp"),
		filepath.Join("/usr", "local", "bin", "omp"),
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return paths
	}
	return append(paths,
		filepath.Join(home, ".local", "bin", "omp"),
		filepath.Join(home, ".bun", "bin", "omp"),
		filepath.Join(home, ".npm-global", "bin", "omp"),
	)
}
