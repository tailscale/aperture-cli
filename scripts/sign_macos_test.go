package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the release hook with fake Apple tools. Signing and notarization
// still need a credentialed macOS run; these checks cover publication gating.
func TestSignMacOS(t *testing.T) {
	// Go's test cache must track the script, which is otherwise read by Bash.
	if _, err := os.ReadFile("sign-macos.sh"); err != nil {
		t.Fatal(err)
	}
	type testCase struct {
		name      string
		target    string
		ready     bool
		status    string
		failure   string
		wantError bool
	}
	tests := []testCase{
		{name: "linux", target: "linux_amd64_v1"},
		{name: "keychain not prepared", target: "darwin_arm64_v8.0"},
		{name: "accepted", ready: true, status: "Accepted"},
		{name: "rejected", ready: true, status: "Invalid", wantError: true},
		{name: "pending", ready: true, status: "In Progress", wantError: true},
		{name: "missing status", ready: true, status: "", wantError: true},
		{name: "notary failure", ready: true, failure: "notary", status: "Accepted", wantError: true},
		{name: "signing failure", ready: true, failure: "sign", wantError: true},
		{name: "verification failure", ready: true, failure: "verify", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			binDir := filepath.Join(dir, "tools")
			if err := os.Mkdir(binDir, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"codesign", "xcrun"} {
				if err := os.WriteFile(filepath.Join(binDir, name), []byte(fakeAppleTool), 0700); err != nil {
					t.Fatal(err)
				}
			}
			binary := filepath.Join(dir, "aperture")
			if err := os.WriteFile(binary, []byte("unsigned\n"), 0700); err != nil {
				t.Fatal(err)
			}
			target := tt.target
			if target == "" {
				target = "darwin_amd64_v1"
			}
			cmd := exec.Command("bash", "sign-macos.sh", binary, target)
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_LOG="+filepath.Join(dir, "commands"),
				"TEST_ARCHIVE_CONTENTS="+filepath.Join(dir, "archived-binary"),
				"TEST_BINARY="+binary, "TEST_FAILURE="+tt.failure, "TEST_STATUS="+tt.status,
			)
			if tt.ready {
				cmd.Env = append(cmd.Env, "APERTURE_SIGNING_READY=1")
			}
			output, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantError {
				t.Fatalf("hook error = %v, want error %v\n%s", err, tt.wantError, output)
			}
			log, err := os.ReadFile(filepath.Join(dir, "commands"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			commands := string(log)
			if !tt.ready {
				if commands != "" {
					t.Errorf("unsigned run must not call Apple tools, got:\n%s", commands)
				}
				contents, readErr := os.ReadFile(binary)
				if readErr != nil || string(contents) != "unsigned\n" {
					t.Errorf("binary must pass through untouched: %q, %v", contents, readErr)
				}
				return
			}
			if tt.failure == "sign" || tt.failure == "verify" {
				if strings.Contains(commands, "xcrun") {
					t.Errorf("submitted a binary after %s failed", tt.failure)
				}
			}
			if tt.status == "Accepted" && tt.failure == "" {
				contents, err := os.ReadFile(filepath.Join(dir, "archived-binary"))
				if err != nil || string(contents) != "unsigned\nsigned\n" {
					t.Errorf("notarization archive must contain the signed binary: %q, %v", contents, err)
				}
				for _, flag := range []string{"--options runtime", "--timestamp", "W5364U7YZB", "--verify --strict", "--wait"} {
					if !strings.Contains(commands, flag) {
						t.Errorf("missing signing requirement %q in:\n%s", flag, commands)
					}
				}
			}
			if _, err := os.Stat(binary + ".zip"); !os.IsNotExist(err) {
				t.Errorf("submission zip was not cleaned up: %v", err)
			}
		})
	}
}

const fakeAppleTool = `#!/usr/bin/env bash
set -euo pipefail
tool=$(basename "$0")
printf '%s %s\n' "$tool" "$*" >> "$TEST_LOG"
case "$tool $1" in
  "codesign --sign")
    [[ "$TEST_FAILURE" != sign ]]
    printf 'signed\n' >> "$TEST_BINARY"
    ;;
  "codesign --verify")
    [[ "$TEST_FAILURE" != verify ]]
    ;;
  "xcrun notarytool")
    [[ "$TEST_FAILURE" != notary ]]
    unzip -p "$3" aperture > "$TEST_ARCHIVE_CONTENTS"
    printf '{"id":"test-submission","status":"%s"}\n' "$TEST_STATUS"
    ;;
esac
`
