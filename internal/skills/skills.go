// Package skills generates a set of agent skills describing this launcher and
// installs them into the skill directories of the coding agents on this
// machine. One skill covers each component — the launcher itself, agents,
// endpoints, bridges, and troubleshooting — so an agent loads only the part it
// needs.
//
// Agents share a single canonical skill root (~/.agents/skills) and each keep
// their own directory of links into it. Installing therefore writes to paths
// that other tools also write to, so every destructive step here is guarded: a
// skill directory is only replaced when it carries our own ownership marker, or
// when the caller explicitly forces it. A directory belonging to another tool —
// including one a user wrote by hand, which has no marker at all — is reported
// as a conflict and left untouched.
package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// markerName is the file written inside an installed skill directory recording
// which tool created it.
const markerName = ".installed-by.json"

// toolName is the value written to, and expected in, the ownership marker.
const toolName = "aperture"

// marker is the on-disk ownership record.
type marker struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
}

// Options controls an install.
type Options struct {
	// Version is recorded in the ownership marker.
	Version string

	// Skills are the skills to install. Any skill this tool installed
	// previously that is absent here is pruned.
	Skills []Skill

	// Project installs into the working directory instead of the home
	// directory.
	Project bool

	// Force replaces skill directories owned by another tool. Without it,
	// such directories are reported as conflicts and nothing is written.
	Force bool

	// Home overrides the home directory. Empty means os.UserHomeDir.
	Home string

	// Cwd overrides the working directory for project installs. Empty means
	// os.Getwd.
	Cwd string
}

// Conflict is a destination holding a skill directory owned by another tool.
type Conflict struct {
	// Path is the directory that would have been replaced.
	Path string

	// Owner is the tool named in the directory's marker, or "unknown" when
	// the directory has no marker.
	Owner string
}

// Link is an installed link from one agent's skill directory to a canonical
// skill directory.
type Link struct {
	// Agent is the user-visible agent name. Empty for the canonical
	// directory itself.
	Agent string

	// Path is the directory linking to the canonical skill.
	Path string
}

// Installed is one skill written by an install.
type Installed struct {
	// Name is the skill directory name.
	Name string

	// Canonical is the shared directory holding the content.
	Canonical string

	// Linked lists the per-agent directories linking to Canonical.
	Linked []Link
}

// Result reports what an install wrote.
type Result struct {
	// Skills are the skills installed, in generation order.
	Skills []Installed

	// Pruned lists directories removed because they hold a skill this tool
	// installed previously but no longer generates.
	Pruned []string

	// Overwrote lists directories that belonged to another tool and were
	// replaced anyway. Only populated when Force is set.
	Overwrote []Conflict
}

// agent is one coding agent's skill directory layout.
type agent struct {
	// name is the user-visible agent name.
	name string

	// globalDir is the agent's skill directory under the home directory.
	globalDir string

	// projectDir is the agent's skill directory relative to a project root.
	projectDir string
}

// agents lists the agents whose skill directories we install into. The
// canonical root is handled separately; these are the per-agent link targets.
func agents() []agent {
	return []agent{
		{name: "Claude Code", globalDir: ".claude/skills", projectDir: ".claude/skills"},
		{name: "Codex", globalDir: ".codex/skills", projectDir: ".codex/skills"},
		{name: "Gemini CLI", globalDir: ".gemini/skills", projectDir: ".gemini/skills"},
		{name: "GitHub Copilot CLI", globalDir: ".copilot/skills", projectDir: ".copilot/skills"},
		{name: "OpenCode", globalDir: ".config/opencode/skills", projectDir: ".opencode/skills"},
	}
}

// ErrConflict is returned when a destination is owned by another tool and Force
// was not set.
var ErrConflict = errors.New("skill directory owned by another tool")

// ConflictError carries the destinations that blocked an install.
type ConflictError struct {
	Conflicts []Conflict
}

func (e *ConflictError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d skill ", len(e.Conflicts))
	if len(e.Conflicts) == 1 {
		b.WriteString("directory is owned by another tool; installing would delete it:\n")
	} else {
		b.WriteString("directories are owned by another tool; installing would delete them:\n")
	}
	for _, c := range e.Conflicts {
		fmt.Fprintf(&b, "  %s (owner: %s)\n", c.Path, c.Owner)
	}
	b.WriteString("Remove them first, or pass -force to overwrite.")
	return b.String()
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

// Install writes every skill to the canonical root and links each into the
// agent directories present on this machine.
//
// It returns a *ConflictError without writing anything when any destination is
// owned by another tool and opts.Force is false.
func Install(opts Options) (Result, error) {
	root, err := installRoot(opts)
	if err != nil {
		return Result{}, err
	}
	canonicalRoot := filepath.Join(root, ".agents", "skills")

	// Check every destination of every skill before writing any of them, so
	// a conflict in the last skill cannot leave the first ones half-applied.
	var conflicts []Conflict
	for _, s := range opts.Skills {
		for _, dest := range destinationsFor(root, canonicalRoot, s.Name, opts.Project) {
			if c, ok := conflictAt(dest.Path); ok {
				conflicts = append(conflicts, c)
			}
		}
	}
	if len(conflicts) > 0 && !opts.Force {
		return Result{}, &ConflictError{Conflicts: conflicts}
	}

	res := Result{Overwrote: conflicts}

	for _, s := range opts.Skills {
		canonical := filepath.Join(canonicalRoot, s.Name)
		if err := writeSkill(canonical, s.Body, opts.Version); err != nil {
			return res, err
		}
		installed := Installed{Name: s.Name, Canonical: canonical}

		for _, dest := range destinationsFor(root, canonicalRoot, s.Name, opts.Project) {
			if dest.Path == canonical {
				continue
			}
			if err := link(canonical, dest.Path); err != nil {
				return res, fmt.Errorf("linking %s: %w", dest.Path, err)
			}
			installed.Linked = append(installed.Linked, dest)
		}
		res.Skills = append(res.Skills, installed)
	}

	pruned, err := prune(root, canonicalRoot, opts)
	if err != nil {
		return res, err
	}
	res.Pruned = pruned

	return res, nil
}

// prune removes skill directories this tool installed previously but no longer
// generates, such as one renamed between releases. Only directories carrying
// our own marker are removed, so a name we no longer use can never take another
// tool's directory with it.
func prune(root, canonicalRoot string, opts Options) ([]string, error) {
	current := make([]string, 0, len(opts.Skills))
	for _, s := range opts.Skills {
		current = append(current, s.Name)
	}

	entries, err := os.ReadDir(canonicalRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var pruned []string
	for _, entry := range entries {
		name := entry.Name()
		if slices.Contains(current, name) {
			continue
		}
		canonical := filepath.Join(canonicalRoot, name)
		if ownerOf(canonical) != toolName {
			continue
		}
		if err := remove(canonical); err != nil {
			return pruned, err
		}
		pruned = append(pruned, canonical)

		// Remove the links that pointed at it, but only while they are
		// still symlinks — a real directory there belongs to someone else.
		for _, dest := range destinationsFor(root, canonicalRoot, name, opts.Project) {
			if dest.Path == canonical {
				continue
			}
			info, err := os.Lstat(dest.Path)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			if err := remove(dest.Path); err != nil {
				return pruned, err
			}
			pruned = append(pruned, dest.Path)
		}
	}
	return pruned, nil
}

// installRoot resolves the base directory an install writes under.
func installRoot(opts Options) (string, error) {
	if opts.Project {
		if opts.Cwd != "" {
			return opts.Cwd, nil
		}
		return os.Getwd()
	}
	if opts.Home != "" {
		return opts.Home, nil
	}
	return os.UserHomeDir()
}

// destinationsFor lists the canonical directory for a skill plus one directory
// per agent whose skill directory already exists, meaning the agent is present.
func destinationsFor(root, canonicalRoot, skillName string, project bool) []Link {
	canonical := filepath.Join(canonicalRoot, skillName)
	dests := []Link{{Path: canonical}}

	for _, a := range agents() {
		rel := a.globalDir
		if project {
			rel = a.projectDir
		}
		parent := filepath.Join(root, rel)
		if _, err := os.Stat(parent); err != nil {
			continue
		}
		dest := filepath.Join(parent, skillName)
		if dest != canonical {
			dests = append(dests, Link{Agent: a.name, Path: dest})
		}
	}
	return dests
}

// conflictAt reports whether a destination holds a skill directory that another
// tool owns. A missing path is free, and a symlink is one of our own links (or
// a link the agent manages), neither of which is another tool's content.
func conflictAt(path string) (Conflict, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return Conflict{}, false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Conflict{}, false
	}
	switch owner := ownerOf(path); owner {
	case toolName:
		return Conflict{}, false
	case "":
		return Conflict{Path: path, Owner: "unknown"}, true
	default:
		return Conflict{Path: path, Owner: owner}, true
	}
}

// ownerOf returns the tool named in a directory's marker, or "" when the
// directory has no readable marker.
func ownerOf(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, markerName))
	if err != nil {
		return ""
	}
	var m marker
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	return m.Tool
}

// writeSkill replaces a canonical directory with fresh content and claims it.
func writeSkill(canonical, body, version string) error {
	if err := remove(canonical); err != nil {
		return err
	}
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(canonical, "SKILL.md"), []byte(body), 0o644); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(marker{Tool: toolName, Version: version}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(canonical, markerName), append(raw, '\n'), 0o644)
}

// link points dest at canonical, preferring a symlink and falling back to a
// copy on platforms or filesystems that refuse one.
func link(canonical, dest string) error {
	if err := remove(dest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	target := canonical
	if rel, err := filepath.Rel(filepath.Dir(dest), canonical); err == nil {
		target = rel
	}
	if err := os.Symlink(target, dest); err == nil {
		return nil
	} else if runtime.GOOS != "windows" && !errors.Is(err, os.ErrPermission) {
		return err
	}
	return copyDir(canonical, dest)
}

// remove deletes a file, symlink, or directory, treating a missing path as
// success. Symlinks are unlinked rather than followed, so removing a link never
// touches the directory it points at.
func remove(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

// copyDir recursively copies src to dst.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		s := filepath.Join(src, entry.Name())
		d := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
			continue
		}
		raw, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}
