package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type standaloneInstall struct {
	binaryPath string
	root       string
}

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

// findStandaloneInstall recognizes only the symlink layout created by
// chatgpt.com/codex/install.sh. It intentionally does not infer ownership from
// the binary's location alone: ~/.local/bin/codex may be managed by the user or
// another installer.
func findStandaloneInstall() (standaloneInstall, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return standaloneInstall{}, false
	}

	binaryPaths := []string{filepath.Join(home, ".local", "bin", "codex")}
	if installDir := os.Getenv("CODEX_INSTALL_DIR"); installDir != "" {
		binaryPaths = append(binaryPaths, filepath.Join(installDir, "codex"))
	}
	if path, err := exec.LookPath(binaryName); err == nil {
		binaryPaths = append(binaryPaths, path)
	}

	roots := []string{filepath.Join(home, ".codex", "packages", "standalone")}
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		roots = append(roots, filepath.Join(codexHome, "packages", "standalone"))
	}

	for _, binaryPath := range binaryPaths {
		for _, root := range roots {
			if symlinkTargetsWithin(binaryPath, root) {
				return standaloneInstall{binaryPath: binaryPath, root: root}, true
			}
		}
	}
	return standaloneInstall{}, false
}

func symlinkTargetsWithin(path, root string) bool {
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}

	root, err = filepath.Abs(root)
	if err != nil {
		return false
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (install standaloneInstall) remove() error {
	// Revalidate immediately before removing anything so a changed or
	// hand-edited symlink cannot cause an unrelated package tree to be deleted.
	if !symlinkTargetsWithin(install.binaryPath, install.root) {
		return fmt.Errorf("Codex installation at %s is no longer a standalone installer symlink", install.binaryPath)
	}
	if err := os.Remove(install.binaryPath); err != nil {
		return fmt.Errorf("remove Codex command: %w", err)
	}

	// The macOS standalone package may install this companion command. Leave a
	// user-managed file at the same path untouched.
	codeModeHost := filepath.Join(filepath.Dir(install.binaryPath), "codex-code-mode-host")
	if symlinkTargetsWithin(codeModeHost, install.root) {
		if err := os.Remove(codeModeHost); err != nil {
			return fmt.Errorf("remove Codex code-mode host command: %w", err)
		}
	}
	if err := os.RemoveAll(install.root); err != nil {
		return fmt.Errorf("remove Codex standalone package cache: %w", err)
	}
	return nil
}
