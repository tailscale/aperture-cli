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

// cleanSigningEnv is the test process environment minus anything a developer
// shell or a CI job exports for the real signing hook, so a run is decided by
// the test's own settings.
func cleanSigningEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "WINDOWS_SIGN_") || strings.HasPrefix(kv, "JSIGN_") || strings.HasPrefix(kv, "APERTURE_WINDOWS_SIGNING_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

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
		omit      string // environment variable the workflow would normally set
		certfile  string // overrides WINDOWS_SIGN_CERTFILE, relative to the test dir
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
		{name: "missing storepass", ready: true, omit: "WINDOWS_SIGN_STOREPASS", wantError: true},
		{name: "missing jar", ready: true, omit: "JSIGN_JAR", wantError: true},
		{name: "missing storetype", ready: true, omit: "WINDOWS_SIGN_STORETYPE", wantError: true},
		{name: "missing keystore", ready: true, omit: "WINDOWS_SIGN_KEYSTORE", wantError: true},
		{name: "optional alias omitted", ready: true, omit: "WINDOWS_SIGN_ALIAS"},
		{name: "certfile that does not exist", ready: true, certfile: "missing.pem", wantError: true},
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
			env := append(cleanSigningEnv(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_LOG="+filepath.Join(dir, "commands"),
				"TEST_FAILURE="+tt.failure,
			)
			if tt.ready {
				env = append(env, "APERTURE_WINDOWS_SIGNING_READY=1")
			}
			certfile := filepath.Join(dir, "chain.pem")
			if err := os.WriteFile(certfile, []byte("chain\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if tt.certfile != "" {
				certfile = filepath.Join(dir, tt.certfile)
			}
			for _, kv := range []string{
				"WINDOWS_SIGN_STORETYPE=GOOGLECLOUD",
				"WINDOWS_SIGN_KEYSTORE=projects/p/locations/l/keyRings/r",
				"WINDOWS_SIGN_ALIAS=key/cryptoKeyVersions/1",
				"WINDOWS_SIGN_CERTFILE=" + certfile,
				"WINDOWS_SIGN_STOREPASS=" + token,
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
			wants := []string{
				"--storetype GOOGLECLOUD",
				"--keystore projects/p/locations/l/keyRings/r",
				"--alias key/cryptoKeyVersions/1",
				"--certfile " + filepath.Join(dir, "chain.pem"),
				"--storepass env:WINDOWS_SIGN_STOREPASS",
				"--tsmode RFC3161",
				binary,
			}
			if tt.omit == "WINDOWS_SIGN_ALIAS" {
				wants = wants[:2]
				if strings.Contains(commands, "--alias") {
					t.Errorf("an unset alias must not be passed to the signer:\n%s", commands)
				}
			}
			for _, want := range wants {
				if !strings.Contains(commands, want) {
					t.Errorf("signer invocation lacks %q:\n%s", want, commands)
				}
			}
		})
	}
}

// The signer runs with the signing credential in its environment, so setup
// must refuse a jar that is not the pinned build, and say so, before it
// exports readiness.
func TestSetupWindowsSigningRejectsUnpinnedJsign(t *testing.T) {
	if _, err := os.ReadFile("setup-windows-signing.sh"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "tools")
	if err := os.Mkdir(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Writes whatever the pinned digest does not match to the -o target.
	curl := "#!/usr/bin/env bash\nwhile [[ $# -gt 0 ]]; do [[ \"$1\" == -o ]] && printf tampered > \"$2\"; shift; done\n"
	if err := os.WriteFile(filepath.Join(binDir, "curl"), []byte(curl), 0700); err != nil {
		t.Fatal(err)
	}
	githubEnv := filepath.Join(dir, "github-env")
	cmd := exec.Command("bash", "setup-windows-signing.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RUNNER_TEMP="+dir,
		"GITHUB_ENV="+githubEnv,
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("setup accepted a jar that does not match the pinned digest\n%s", output)
	}
	if !strings.Contains(string(output), "pinned sha256") {
		t.Errorf("setup failed for another reason:\n%s", output)
	}
	if exported, err := os.ReadFile(githubEnv); err == nil && len(exported) > 0 {
		t.Errorf("a rejected setup must export nothing, got:\n%s", exported)
	}
}

// The self-signed identity stands in for the production key in CI, so it has
// to be what Authenticode demands of a signing certificate: a code-signing
// extended key usage, and a PKCS#12 that opens with the password it was given
// and holds both the key and the certificate.
func TestMakeSelfSignedCodesign(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	if _, err := os.ReadFile("make-selfsigned-codesign.sh"); err != nil {
		t.Fatal(err)
	}
	for _, alg := range []string{"ec", "rsa"} {
		t.Run(alg, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			cmd := exec.Command("bash", "make-selfsigned-codesign.sh", dir, alg)
			cmd.Env = append(os.Environ(), "CODESIGN_P12_PASSWORD=test-password")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generate: %v\n%s", err, output)
			}
			for _, leaked := range []string{"codesign.key", "codesign.cnf"} {
				if _, err := os.Stat(filepath.Join(dir, leaked)); !os.IsNotExist(err) {
					t.Errorf("%s was left behind: %v", leaked, err)
				}
			}
			cert, err := exec.Command("openssl", "x509", "-in", filepath.Join(dir, "codesign.crt"), "-noout", "-text").CombinedOutput()
			if err != nil {
				t.Fatalf("read certificate: %v\n%s", err, cert)
			}
			if !strings.Contains(string(cert), "Code Signing") {
				t.Errorf("certificate lacks the code-signing extended key usage:\n%s", cert)
			}
			p12 := filepath.Join(dir, "codesign.p12")
			opened, err := exec.Command("openssl", "pkcs12", "-in", p12, "-passin", "pass:test-password", "-nodes").CombinedOutput()
			if err != nil {
				t.Fatalf("open pkcs12 with its password: %v\n%s", err, opened)
			}
			if !strings.Contains(string(opened), "PRIVATE KEY") || !strings.Contains(string(opened), "BEGIN CERTIFICATE") {
				t.Errorf("pkcs12 must hold the key and the certificate:\n%s", opened)
			}
			if exec.Command("openssl", "pkcs12", "-in", p12, "-passin", "pass:wrong", "-nodes").Run() == nil {
				t.Error("pkcs12 opened with the wrong password")
			}
		})
	}
}
