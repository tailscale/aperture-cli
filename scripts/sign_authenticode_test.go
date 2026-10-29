package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeJava stands in for the JVM that runs Jsign. It records its arguments
// and, like Jsign, appends a signature to the file it was asked to sign.
const fakeJava = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TEST_LOG"
[[ "$TEST_FAILURE" != sign ]] || exit 1
printf 'signed\n' >> "${@: -1}"
`

// Exercise the release hook with a fake JVM. Real signing needs an Azure
// login; these checks cover publication gating and that the token never
// appears on the command line.
func TestSignWindows(t *testing.T) {
	// Go's test cache must track the script, which is otherwise read by Bash.
	if _, err := os.ReadFile("sign-windows.sh"); err != nil {
		t.Fatal(err)
	}
	type testCase struct {
		name      string
		target    string
		ready     bool
		snapshot  string
		failure   string
		omit      string // environment variable setup would normally export
		wantError bool
	}
	tests := []testCase{
		{name: "linux", target: "linux_amd64_v1", ready: true},
		{name: "darwin", target: "darwin_arm64_v8.0", ready: true},
		{name: "windows amd64", ready: true},
		{name: "windows arm64", target: "windows_arm64_v8.0", ready: true},
		{name: "windows explicit false snapshot", ready: true, snapshot: "false"},
		{name: "missing readiness omitted snapshot", wantError: true},
		{name: "missing readiness explicit false", snapshot: "false", wantError: true},
		{name: "snapshot true missing readiness", snapshot: "true"},
		{name: "snapshot true with readiness", ready: true, snapshot: "true"},
		{name: "missing token", ready: true, omit: "ARTIFACT_SIGNING_TOKEN", wantError: true},
		{name: "missing jar", ready: true, omit: "JSIGN_JAR", wantError: true},
		{name: "missing endpoint", ready: true, omit: "ARTIFACT_SIGNING_ENDPOINT", wantError: true},
		{name: "missing account", ready: true, omit: "ARTIFACT_SIGNING_ACCOUNT", wantError: true},
		{name: "missing profile", ready: true, omit: "ARTIFACT_SIGNING_PROFILE", wantError: true},
		{name: "signing failure", ready: true, failure: "sign", wantError: true},
	}
	const token = "test-access-token"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			binDir := filepath.Join(dir, "tools")
			if err := os.Mkdir(binDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(binDir, "java"), []byte(fakeJava), 0700); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(dir, "aperture.exe")
			if err := os.WriteFile(binary, []byte("unsigned\n"), 0700); err != nil {
				t.Fatal(err)
			}
			target := tt.target
			if target == "" {
				target = "windows_amd64_v1"
			}
			args := []string{"sign-windows.sh", binary, target}
			if tt.snapshot != "" {
				args = append(args, tt.snapshot)
			}
			cmd := exec.Command("bash", args...)
			env := append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_LOG="+filepath.Join(dir, "commands"),
				"TEST_FAILURE="+tt.failure,
			)
			if tt.ready {
				env = append(env, "APERTURE_WINDOWS_SIGNING_READY=1")
			}
			for _, kv := range []string{
				"ARTIFACT_SIGNING_ENDPOINT=weu.codesigning.azure.net",
				"ARTIFACT_SIGNING_ACCOUNT=test-account",
				"ARTIFACT_SIGNING_PROFILE=test-profile",
				"ARTIFACT_SIGNING_TOKEN=" + token,
				"JSIGN_JAR=" + filepath.Join(dir, "jsign.jar"),
			} {
				if tt.omit == "" || !strings.HasPrefix(kv, tt.omit+"=") {
					env = append(env, kv)
				}
			}
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			if (err != nil) != tt.wantError {
				t.Fatalf("hook error = %v, want error %v\n%s", err, tt.wantError, output)
			}
			log, err := os.ReadFile(filepath.Join(dir, "commands"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			commands := string(log)
			contents, err := os.ReadFile(binary)
			if err != nil {
				t.Fatal(err)
			}

			signs := strings.HasPrefix(target, "windows_") && tt.snapshot != "true"
			if !signs {
				if commands != "" {
					t.Errorf("skipped run must not call the signer, got:\n%s", commands)
				}
				if string(contents) != "unsigned\n" {
					t.Errorf("binary must pass through untouched: %q", contents)
				}
				return
			}
			if tt.wantError {
				if tt.failure == "" && commands != "" {
					t.Errorf("a release that is not ready must not reach the signer, got:\n%s", commands)
				}
				if string(contents) != "unsigned\n" {
					t.Errorf("a failed run must not leave a signed binary behind: %q", contents)
				}
				return
			}
			if string(contents) != "unsigned\nsigned\n" {
				t.Errorf("binary was not signed: %q", contents)
			}
			if strings.Contains(commands+string(output), token) {
				t.Errorf("access token leaked into the command line or output:\n%s%s", commands, output)
			}
			for _, want := range []string{
				"--storetype TRUSTEDSIGNING",
				"--keystore weu.codesigning.azure.net",
				"--alias test-account/test-profile",
				"--storepass env:ARTIFACT_SIGNING_TOKEN",
				"--tsmode RFC3161",
				binary,
			} {
				if !strings.Contains(commands, want) {
					t.Errorf("signer invocation lacks %q:\n%s", want, commands)
				}
			}
		})
	}
}

// The signer runs with the access token in its environment, so setup must
// refuse a jar that is not the pinned build, and say so, before any token is
// requested or exported.
func TestSetupWindowsSigningRejectsUnpinnedJsign(t *testing.T) {
	if _, err := os.ReadFile("setup-windows-signing.sh"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "tools")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	fakes := map[string]string{
		// Writes whatever the pinned digest does not match to the -o target.
		"curl": "#!/usr/bin/env bash\nwhile [[ $# -gt 0 ]]; do [[ \"$1\" == -o ]] && printf tampered > \"$2\"; shift; done\n",
		"az":   "#!/usr/bin/env bash\necho az >> \"$TEST_LOG\"\necho secret-token\n",
	}
	for name, body := range fakes {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	githubEnv := filepath.Join(dir, "github-env")
	cmd := exec.Command("bash", "setup-windows-signing.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TEST_LOG="+filepath.Join(dir, "commands"),
		"RUNNER_TEMP="+dir,
		"GITHUB_ENV="+githubEnv,
		"ARTIFACT_SIGNING_ENDPOINT=weu.codesigning.azure.net",
		"ARTIFACT_SIGNING_ACCOUNT=test-account",
		"ARTIFACT_SIGNING_PROFILE=test-profile",
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("setup accepted a jar that does not match the pinned digest\n%s", output)
	}
	if !strings.Contains(string(output), "pinned sha256") {
		t.Errorf("setup failed for another reason:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(dir, "commands")); !os.IsNotExist(err) {
		t.Errorf("setup requested a token before verifying the jar: %v", err)
	}
	if exported, err := os.ReadFile(githubEnv); err == nil && len(exported) > 0 {
		t.Errorf("a rejected setup must export nothing, got:\n%s", exported)
	}
}
