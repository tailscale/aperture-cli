package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

// TestOpenRunLogAppendsThenTruncates covers the two things the run log has to
// get right to be readable: a run does not erase the one before it, and the
// file cannot grow forever on a box where nothing prunes it.
func TestOpenRunLogAppendsThenTruncates(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	write := func(s string) {
		f, err := config.OpenRunLog()
		if err != nil {
			t.Fatalf("OpenRunLog: %v", err)
		}
		if _, err := f.WriteString(s); err != nil {
			t.Fatalf("write: %v", err)
		}
		f.Close()
	}

	write("first run\n")
	write("second run\n")

	path, err := config.RunLogPath()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := "first run\nsecond run\n"; string(got) != want {
		t.Errorf("log = %q, want %q: a run erased the one that failed before it", got, want)
	}

	if err := os.WriteFile(path, []byte(strings.Repeat("x", (2<<20)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	write("after the cap\n")
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "after the cap\n" {
		t.Errorf("log is %d bytes, want the oversized file started over", len(got))
	}
}
