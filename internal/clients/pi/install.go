package pi

import (
	"os"
	"path/filepath"
)

// commonBinaryPaths returns the non-PATH locations where pi is commonly
// installed. Homebrew's npm prefix is listed because the pi.dev installer
// and `npm install -g` both land there on a Homebrew-managed Node, which is
// not always on PATH in a fresh shell.
func commonBinaryPaths() []string {
	paths := []string{
		filepath.Join("/opt", "homebrew", "bin", "pi"),
		filepath.Join("/usr", "local", "bin", "pi"),
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return paths
	}
	return append(paths,
		filepath.Join(home, ".pi", "bin", "pi"),
		filepath.Join(home, ".npm-global", "bin", "pi"),
	)
}
