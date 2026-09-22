package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var apertureBin string

// TestMain builds the binary under test once. The tests exercise what we
// ship, so they run the real build rather than recompiling main's guts
// into the test binary.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aperture-e2e")
	if err != nil {
		panic(err)
	}
	apertureBin = filepath.Join(dir, "aperture")
	build := exec.Command("go", "build", "-o", apertureBin, "../cmd/aperture")
	if out, err := build.CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// modelsJSON is the smallest GET /v1/models payload that walks one provider
// through discovery: one provider, one model, one wire endpoint. With one
// of each, the launch flow asks no follow-up questions — selecting the
// client launches it.
const modelsJSON = `{
  "object": "list",
  "data": [
    {
      "id": "test-model",
      "supported_endpoints": ["/v1/responses"],
      "metadata": {"provider": {"id": "test-provider", "name": "Test Provider", "upstream": "test"}}
    }
  ]
}`

// fakeAperture serves the discovery contract and nothing else.
func fakeAperture(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, modelsJSON)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVersion(t *testing.T) {
	stdout, _, err := run(t, apertureBin, hermeticEnv(t, ""), "-version")
	if err != nil {
		t.Fatalf("-version: %v", err)
	}
	if stdout == "" || stdout == "B0-dev" {
		t.Errorf("version output = %q, want a release version", stdout)
	}
}

// A bad endpoint URL has to fail before the TUI takes the terminal: the
// script that passed it reads stderr and the exit code, not a painted error.
func TestBadEndpointExitsBeforeTUI(t *testing.T) {
	_, stderr, err := run(t, apertureBin, hermeticEnv(t, ""), "-endpoint", "://nope")
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("exit = %v, want exit code 1", err)
	}
	if !strings.Contains(stderr, "aperture:") {
		t.Errorf("stderr = %q, want the failure reported", stderr)
	}
}

// The happy path: connect to a fake Aperture, pick the only installed
// client, and watch the launch reach the stub binary with the generated
// provider extension pointing back at the fake Aperture.
func TestLaunchPi(t *testing.T) {
	srv := fakeAperture(t)
	binDir := t.TempDir()
	recordDir := installStubPi(t, binDir)
	env := append(hermeticEnv(t, binDir), "APERTURE_E2E_RECORD="+recordDir)

	term := spawn(t, apertureBin, []string{"-endpoint", srv.URL}, env)
	// With the stub as the only installed client, the picker opens with Pi
	// as row [1] and no quick-select.
	term.waitFor(t, "Which editor do you want to use?")
	term.waitFor(t, "[1] Pi")
	term.send("1")

	// One provider, one backend, one model: no follow-up menus, the
	// selection launches straight into the stub.
	argv := waitForFile(t, filepath.Join(recordDir, "argv"))
	extension := waitForFile(t, filepath.Join(recordDir, "extension"))

	// "q" quits only from the root menu: a clean exit here also proves the
	// TUI took the terminal back after the child exited.
	term.send("q")
	term.waitExit(t)

	if !strings.Contains(argv, "-e\n") {
		t.Errorf("stub argv = %q, want the extension loaded with -e", argv)
	}
	if !strings.Contains(extension, srv.URL) {
		t.Errorf("extension routes to %q, want the Aperture at %s", extension, srv.URL)
	}
}

// An unreachable endpoint paints the failure banner rather than hanging or
// exiting; the launcher stays up so the user can pick another endpoint.
func TestUnreachableEndpoint(t *testing.T) {
	term := spawn(t, apertureBin, []string{"-endpoint", "http://127.0.0.1:1"}, hermeticEnv(t, ""))
	term.waitFor(t, "Could not reach")
	term.send("\x03")
	term.waitExit(t)
}
