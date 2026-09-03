package skills

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeMarker plants an ownership marker naming the given tool.
func writeMarker(t *testing.T, dir, tool string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(marker{Tool: tool, Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, markerName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// testSkills is a small stand-in for the generated set.
func testSkills() []Skill {
	return []Skill{
		{Name: "aperture", Body: "core"},
		{Name: "aperture-agents", Body: "agents"},
	}
}

func TestConflictAtUnmarkedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "aperture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, ok := conflictAt(dir)
	if !ok {
		t.Fatal("an unmarked directory must be treated as another tool's")
	}
	if c.Owner != "unknown" {
		t.Errorf("owner = %q, want %q", c.Owner, "unknown")
	}
}

func TestConflictAtOwnDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "aperture")
	writeMarker(t, dir, toolName)

	if _, ok := conflictAt(dir); ok {
		t.Fatal("a directory we installed must be replaceable")
	}
}

func TestConflictAtForeignDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "aperture")
	writeMarker(t, dir, "some-other-tool")

	c, ok := conflictAt(dir)
	if !ok {
		t.Fatal("another tool's directory must be a conflict")
	}
	if c.Owner != "some-other-tool" {
		t.Errorf("owner = %q, want %q", c.Owner, "some-other-tool")
	}
}

func TestConflictAtMissingDirectory(t *testing.T) {
	if _, ok := conflictAt(filepath.Join(t.TempDir(), "absent")); ok {
		t.Fatal("a missing directory must not be a conflict")
	}
}

func TestInstallWritesEverySkill(t *testing.T) {
	home := t.TempDir()

	res, err := Install(Options{Home: home, Skills: testSkills(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skills) != 2 {
		t.Fatalf("installed %d skills, want 2", len(res.Skills))
	}

	for _, s := range testSkills() {
		dir := filepath.Join(home, ".agents", "skills", s.Name)
		raw, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		if string(raw) != s.Body {
			t.Errorf("%s content = %q, want %q", s.Name, raw, s.Body)
		}
		if owner := ownerOf(dir); owner != toolName {
			t.Errorf("%s owner = %q, want %q", s.Name, owner, toolName)
		}
	}
}

func TestInstallRefusesForeignDirectory(t *testing.T) {
	home := t.TempDir()
	// The conflict is on the second skill, so the first would already have
	// been written if the check were not done up front.
	foreign := filepath.Join(home, ".agents", "skills", "aperture-agents")
	writeMarker(t, foreign, "some-other-tool")
	if err := os.WriteFile(filepath.Join(foreign, "SKILL.md"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Install(Options{Home: home, Skills: testSkills(), Version: "test"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	raw, readErr := os.ReadFile(filepath.Join(foreign, "SKILL.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != "keep me" {
		t.Errorf("content = %q, want %q — a refused install must not write", raw, "keep me")
	}

	// Nothing at all should have been written, including the first skill.
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "aperture")); !os.IsNotExist(err) {
		t.Error("a conflict on a later skill must stop the whole install")
	}
}

func TestInstallForceOverwritesAndReports(t *testing.T) {
	home := t.TempDir()
	foreign := filepath.Join(home, ".agents", "skills", "aperture")
	writeMarker(t, foreign, "some-other-tool")

	res, err := Install(Options{Home: home, Skills: testSkills(), Version: "test", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if ownerOf(foreign) != toolName {
		t.Error("a forced install should claim the directory")
	}
	if len(res.Overwrote) != 1 || res.Overwrote[0].Owner != "some-other-tool" {
		t.Errorf("overwrote = %+v, want one entry owned by some-other-tool", res.Overwrote)
	}
}

func TestInstallLinksDetectedAgentsOnly(t *testing.T) {
	home := t.TempDir()
	// Only Claude Code is "installed" — its skills directory exists.
	claude := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Install(Options{Home: home, Skills: testSkills(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}

	for _, installed := range res.Skills {
		want := filepath.Join(claude, installed.Name)
		if len(installed.Linked) != 1 || installed.Linked[0].Path != want {
			t.Fatalf("%s linked = %v, want exactly [%s]", installed.Name, installed.Linked, want)
		}
		if installed.Linked[0].Agent != "Claude Code" {
			t.Errorf("agent = %q, want %q", installed.Linked[0].Agent, "Claude Code")
		}
		// The link must resolve to the canonical content.
		if _, err := os.ReadFile(filepath.Join(want, "SKILL.md")); err != nil {
			t.Errorf("%s: link does not resolve: %v", installed.Name, err)
		}
	}
}

func TestInstallPrunesOurOwnStaleSkills(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}

	// A skill from a previous release that we no longer generate.
	stale := filepath.Join(home, ".agents", "skills", "aperture-old")
	writeMarker(t, stale, toolName)
	staleLink := filepath.Join(claude, "aperture-old")
	if err := os.Symlink(stale, staleLink); err != nil {
		t.Fatal(err)
	}

	res, err := Install(Options{Home: home, Skills: testSkills(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a stale skill of ours should be pruned")
	}
	if _, err := os.Lstat(staleLink); !os.IsNotExist(err) {
		t.Error("the stale skill's link should be pruned too")
	}
	if len(res.Pruned) != 2 {
		t.Errorf("pruned = %v, want the directory and its link", res.Pruned)
	}
}

func TestInstallNeverPrunesAnotherToolsSkill(t *testing.T) {
	home := t.TempDir()

	// A directory belonging to someone else, which we never generated and
	// must not touch even though it is not in our current set.
	foreign := filepath.Join(home, ".agents", "skills", "someone-elses-skill")
	writeMarker(t, foreign, "some-other-tool")
	if err := os.WriteFile(filepath.Join(foreign, "SKILL.md"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Install(Options{Home: home, Skills: testSkills(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pruned) != 0 {
		t.Errorf("pruned = %v, want nothing", res.Pruned)
	}

	raw, err := os.ReadFile(filepath.Join(foreign, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "keep me" {
		t.Errorf("content = %q — pruning must not reach another tool's skill", raw)
	}
}

func TestInstallDoesNotPruneUnmarkedDirectories(t *testing.T) {
	home := t.TempDir()

	// A hand-written skill: no marker at all.
	handmade := filepath.Join(home, ".agents", "skills", "my-notes")
	if err := os.MkdirAll(handmade, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(handmade, "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(Options{Home: home, Skills: testSkills(), Version: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(handmade, "SKILL.md")); err != nil {
		t.Errorf("an unmarked directory must survive pruning: %v", err)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}

	first, err := Install(Options{Home: home, Skills: testSkills(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Install(Options{Home: home, Skills: testSkills(), Version: "test"})
	if err != nil {
		t.Fatalf("a second install over our own output must succeed: %v", err)
	}

	if len(second.Skills) != len(first.Skills) {
		t.Errorf("second install wrote %d skills, first wrote %d", len(second.Skills), len(first.Skills))
	}
	if len(second.Pruned) != 0 {
		t.Errorf("pruned = %v, want nothing on an unchanged reinstall", second.Pruned)
	}
}

func TestRemoveUnlinksSymlinkWithoutTouchingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "SKILL.md")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := remove(link); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("the symlink should be gone")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("removing a symlink must not touch its target: %v", err)
	}
}
