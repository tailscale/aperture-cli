package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A release may only sign a commit main already contains; a tag on a branch
// must fail before any key is touched.
func TestCheckReleaseOnMain(t *testing.T) {
	script, err := filepath.Abs("check-release-on-main.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(script); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin")
	clone := filepath.Join(dir, "clone")
	env := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	git := func(in string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = in, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.Mkdir(origin, 0700); err != nil {
		t.Fatal(err)
	}
	git(origin, "init", "--quiet", "--initial-branch=main")
	git(origin, "commit", "--quiet", "--allow-empty", "-m", "first")
	first := git(origin, "rev-parse", "HEAD")
	git(origin, "commit", "--quiet", "--allow-empty", "-m", "second")
	git(origin, "switch", "--quiet", "-c", "feature")
	git(origin, "commit", "--quiet", "--allow-empty", "-m", "off main")
	offMain := git(origin, "rev-parse", "HEAD")
	git(origin, "switch", "--quiet", "main")
	git(dir, "clone", "--quiet", "--no-checkout", origin, clone)
	git(clone, "fetch", "--quiet", "origin", "feature")

	tests := []struct {
		name, commit string
		wantError    bool
	}{
		{name: "main head", commit: "origin/main"},
		{name: "older main commit", commit: first},
		{name: "commit only on a branch", commit: offMain, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git(clone, "checkout", "--quiet", "--detach", tt.commit)
			cmd := exec.Command("bash", script)
			cmd.Dir, cmd.Env = clone, env
			output, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantError {
				t.Fatalf("check error = %v, want error %v\n%s", err, tt.wantError, output)
			}
			if tt.wantError && !strings.Contains(string(output), "not on main") {
				t.Errorf("check failed for another reason:\n%s", output)
			}
		})
	}
}
