//go:build unix

package opencode

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

// A write that fails partway through (disk full, quota) must not leave the
// partially written temp file behind in ~/.opencode. Unix-only: the failure
// is induced with RLIMIT_FSIZE and SIGXFSZ, which have no Windows equivalent.
func TestWriteProviderConfigRemovesTempFileOnWriteFailure(t *testing.T) {
	if os.Getenv("TEST_OPENCODE_WRITE_FAILURE") == "1" {
		signal.Ignore(syscall.SIGXFSZ)
		defer signal.Reset(syscall.SIGXFSZ)
		var orig syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &orig); err != nil {
			t.Fatal(err)
		}
		defer syscall.Setrlimit(syscall.RLIMIT_FSIZE, &orig)
		limited := syscall.Rlimit{Cur: 1, Max: orig.Max}
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
			t.Fatal(err)
		}

		provs := config.ProviderInfo{ID: "openai", Models: []string{"gpt-5"}, SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true}}
		if _, _, err := writeProviderConfig(testHost, provs); err == nil {
			t.Fatal("writeProviderConfig: want a write failure under the file-size limit")
		}
		return
	}

	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWriteProviderConfigRemovesTempFileOnWriteFailure$")
	cmd.Env = append(os.Environ(), "TEST_OPENCODE_WRITE_FAILURE=1", "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child process failed: %v\n%s", err, out)
	}

	entries, err := os.ReadDir(filepath.Join(home, ".opencode"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tmp_aperture_config_") {
			t.Errorf("leaked temp file %s after a write failure", e.Name())
		}
	}
}
