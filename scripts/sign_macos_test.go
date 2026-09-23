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
		snapshot  string
		status    string
		response  string
		failure   string
		wantError bool
	}
	tests := []testCase{
		{name: "linux", target: "linux_amd64_v1", ready: true},
		{name: "darwin missing readiness omitted snapshot", wantError: true},
		{name: "darwin missing readiness explicit false", snapshot: "false", wantError: true},
		{name: "snapshot true missing readiness", snapshot: "true"},
		{name: "snapshot true with readiness", ready: true, snapshot: "true"},
		{name: "accepted compact", ready: true, response: `{"status":"Accepted"}`},
		{name: "accepted pretty", ready: true, snapshot: "false", response: "{\n  \"status\" : \"Accepted\"\n}"},
		{name: "invalid", ready: true, response: `{"status":"Invalid"}`, wantError: true},
		{name: "pending", ready: true, response: `{"status":"In Progress"}`, wantError: true},
		{name: "missing status", ready: true, response: `{"id":"test-submission"}`, wantError: true},
		{name: "null status", ready: true, response: `{"status":null}`, wantError: true},
		{name: "wrong-type status", ready: true, response: `{"status":123}`, wantError: true},
		{name: "malformed json", ready: true, response: `{"status":"Accepted"`, wantError: true},
		{name: "top-level array", ready: true, response: `[{"status":"Accepted"}]`, wantError: true},
		{name: "multiple results", ready: true, response: "{\"status\":\"Invalid\"}\n{\"status\":\"Accepted\"}", wantError: true},
		{name: "nested accepted under invalid", ready: true, response: `{"status":"Invalid","details":{"status":"Accepted"}}`, wantError: true},
		{name: "notary failure before output", ready: true, failure: "notary", status: "Accepted", wantError: true},
		{name: "notary failure after output", ready: true, failure: "notary-after", response: `{"status":"Accepted"}`, wantError: true},
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
			args := []string{"sign-macos.sh", binary, target}
			if tt.snapshot != "" {
				args = append(args, tt.snapshot)
			}
			cmd := exec.Command("bash", args...)
			readyValue := ""
			if tt.ready {
				readyValue = "1"
			}
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_LOG="+filepath.Join(dir, "commands"),
				"TEST_ARCHIVE_CONTENTS="+filepath.Join(dir, "archived-binary"),
				"TEST_BINARY="+binary,
				"TEST_FAILURE="+tt.failure,
				"TEST_STATUS="+tt.status,
				"TEST_RESPONSE="+tt.response,
				"APERTURE_SIGNING_READY="+readyValue,
			)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantError {
				t.Fatalf("hook error = %v, want error %v\n%s", err, tt.wantError, output)
			}
			log, err := os.ReadFile(filepath.Join(dir, "commands"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			commands := string(log)
			if _, err := os.Stat(binary + ".zip"); !os.IsNotExist(err) {
				t.Errorf("submission zip was not cleaned up: %v", err)
			}
			if !tt.ready || tt.snapshot == "true" || strings.HasPrefix(target, "linux_") {
				if commands != "" {
					t.Errorf("skipped run must not call Apple tools, got:\n%s", commands)
				}
				contents, readErr := os.ReadFile(binary)
				if readErr != nil || string(contents) != "unsigned\n" {
					t.Errorf("binary must pass through untouched: %q, %v", contents, readErr)
				}
				return
			}
			if commands == "" {
				t.Fatal("expected Apple tools to be called")
			}
			if tt.failure == "sign" || tt.failure == "verify" {
				if strings.Contains(commands, "xcrun") {
					t.Errorf("submitted a binary after %s failed", tt.failure)
				}
			}
			if !tt.wantError {
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
    if [[ -n "${TEST_RESPONSE:-}" ]]; then
      printf '%s\n' "$TEST_RESPONSE"
    else
      printf '{"id":"test-submission","status":"%s"}\n' "$TEST_STATUS"
    fi
    [[ "$TEST_FAILURE" != notary-after ]]
    ;;
esac
`
