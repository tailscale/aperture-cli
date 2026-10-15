//go:build tailnet

package e2e

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The scripted-surface contract, pinned byte for byte: the columns a script
// parses, the stderr a user reads and the exit codes a supervisor checks.
// These goldens are what make "data goes to stdout, diagnostics to stderr,
// failures exit non-zero" an enforceable contract rather than an aspiration.
// Regenerate with `go test -tags tailnet ./e2e/ -run Contract -update`.

var update = flag.Bool("update", false, "rewrite the contract golden files")

// contractCase is one invocation and what the contract promises for it.
type contractCase struct {
	name string
	args []string
	code int // expected exit code
}

func TestContract(t *testing.T) {
	cases := []struct {
		name  string
		steps []contractCase
	}{
		{
			name: "bridge lifecycle",
			steps: []contractCase{
				{name: "add", args: []string{"bridge", "add", "work", "--port", "8991"}},
				{name: "list", args: []string{"bridge", "list"}},
				{name: "describe", args: []string{"bridge", "describe", "work"}},
				{name: "delete", args: []string{"bridge", "delete", "work"}},
				{name: "list-after-delete", args: []string{"bridge", "list"}},
			},
		},
		{
			name: "endpoint lifecycle",
			steps: []contractCase{
				{name: "add", args: []string{"endpoint", "add", "e1", "http://ai"}},
				{name: "add-second", args: []string{"endpoint", "add", "e2", "http://other"}},
				{name: "list", args: []string{"endpoint", "list"}},
				{name: "delete", args: []string{"endpoint", "delete", "e2"}},
				{name: "list-after-delete", args: []string{"endpoint", "list"}},
			},
		},
		{
			name: "errors",
			steps: []contractCase{
				{name: "serve-no-bridge", args: []string{"serve"}, code: 1},
				{name: "describe-missing", args: []string{"bridge", "describe", "ghost"}, code: 1},
				{name: "delete-missing", args: []string{"endpoint", "delete", "ghost"}, code: 1},
				{name: "add", args: []string{"endpoint", "add", "e1", "http://x"}},
				{name: "add-duplicate", args: []string{"endpoint", "add", "e1", "http://y"}, code: 1},
			},
		},
	}

	for _, suite := range cases {
		t.Run(suite.name, func(t *testing.T) {
			// Each suite gets a fresh home so its steps build on each other.
			suiteEnv := hermeticEnv(t, "")
			for _, step := range suite.steps {
				stdout, stderr, err := run(t, apertureBin, suiteEnv, step.args...)
				code := 0
				if exitErr, ok := err.(*exec.ExitError); ok {
					code = exitErr.ExitCode()
				} else if err != nil {
					t.Fatalf("%s: %v", step.name, err)
				}
				if code != step.code {
					t.Errorf("%s: exit %d, want %d (stderr %q)", step.name, code, step.code, stderr)
				}
				got := fmt.Sprintf("exit %d\n-- stdout --\n%s-- stderr --\n%s", code, normalizeGolden(stdout), normalizeGolden(stderr))
				golden := filepath.Join("testdata", "contract", suite.name+"-"+step.name+".golden")
				if *update {
					if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("reading %s (run with -update to create): %v", golden, err)
				}
				if got != string(want) {
					t.Errorf("%s output changed (-update to accept):\n--- want ---\n%s\n--- got ---\n%s", step.name, want, got)
				}
			}
		})
	}
}
