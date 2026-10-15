package cli

import (
	"strings"
	"testing"
)

func TestBridgeCommands(t *testing.T) {
	scratchHome(t)
	stdout, _, err := runCLI(t, "bridge", "add", "work", "--port", "8991")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(stdout, "work") || !strings.Contains(stdout, "8991") {
		t.Errorf("add printed %q, want the new bridge with its port", stdout)
	}

	// A duplicate name is refused, and the shorthand carries the port.
	if _, _, err := runCLI(t, "bridge", "add", "Work"); err == nil {
		t.Error("a second bridge called Work was accepted")
	}
	if _, _, err := runCLI(t, "bridge", "add", "home", "-p", "8992"); err != nil {
		t.Fatalf("add with shorthand port: %v", err)
	}

	stdout, _, err = runCLI(t, "bridge", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"NAME", "work", "8991", "home", "8992"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list = %q, want %q in it", stdout, want)
		}
	}

	stdout, _, err = runCLI(t, "bridge", "describe", "work")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, want := range []string{"name: work", "port: 8991", "machine slots on disk: none"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("describe = %q, want %q in it", stdout, want)
		}
	}

	// Delete removes a bridge that never started a machine.
	if _, _, err := runCLI(t, "bridge", "delete", "work"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	stdout, _, _ = runCLI(t, "bridge", "list")
	if strings.Contains(stdout, "work") {
		t.Errorf("list = %q after delete, want work gone", stdout)
	}
	if _, _, err := runCLI(t, "bridge", "delete", "work"); err == nil {
		t.Error("deleting a missing bridge did not fail")
	}
}

// A bridge an endpoint uses is refused: deleting it would strand the
// endpoint, and the error has to say why.
func TestBridgeDeleteRefusesABridgeInUse(t *testing.T) {
	scratchHome(t)
	if _, _, err := runCLI(t, "bridge", "add", "work"); err != nil {
		t.Fatalf("bridge add: %v", err)
	}
	if _, _, err := runCLI(t, "endpoint", "add", "e1", "--bridge", "work", "http://ai"); err != nil {
		t.Fatalf("endpoint add: %v", err)
	}
	if _, _, err := runCLI(t, "bridge", "delete", "work"); err == nil {
		t.Error("deleted a bridge an endpoint connects through")
	}
}

func TestBridgeDeleteForceRemovesBridgeAndEndpoints(t *testing.T) {
	scratchHome(t)
	if _, _, err := runCLI(t, "bridge", "add", "work"); err != nil {
		t.Fatalf("bridge add: %v", err)
	}
	if _, _, err := runCLI(t, "endpoint", "add", "e1", "--bridge", "work", "http://ai"); err != nil {
		t.Fatalf("endpoint add: %v", err)
	}
	if _, _, err := runCLI(t, "bridge", "delete", "work"); err == nil {
		t.Fatal("bridge delete without --force succeeded on bridge in use")
	}
	if _, _, err := runCLI(t, "bridge", "delete", "work", "--force"); err != nil {
		t.Fatalf("bridge delete with --force: %v", err)
	}
	stdout, _, _ := runCLI(t, "bridge", "list")
	if strings.Contains(stdout, "work") {
		t.Errorf("bridge list = %q, want work gone", stdout)
	}
	stdout, _, _ = runCLI(t, "endpoint", "list")
	if strings.Contains(stdout, "e1") {
		t.Errorf("endpoint list = %q, want e1 gone", stdout)
	}
}

// endpoint add is a management command: a typo in --bridge must error and
// leave settings untouched, not create a bridge nobody logged in.
func TestEndpointAddRefusesAMissingBridge(t *testing.T) {
	scratchHome(t)
	if _, _, err := runCLI(t, "endpoint", "add", "e1", "--bridge", "workk", "http://ai"); err == nil {
		t.Fatal("endpoint add created a bridge from a typo")
	}
	stdout, _, err := runCLI(t, "bridge", "list")
	if err != nil {
		t.Fatalf("bridge list: %v", err)
	}
	if strings.Contains(stdout, "workk") {
		t.Errorf("bridge list = %q, want nothing created", stdout)
	}
	if strings.Contains(stdout, "workk\t") {
		t.Error("unexpected tab escape")
	}
}

func TestEndpointAddPrintsCanonicalName(t *testing.T) {
	scratchHome(t)
	stdout, _, err := runCLI(t, "endpoint", "add", "  e1  ", "http://ai")
	if err != nil {
		t.Fatalf("endpoint add: %v", err)
	}
	if strings.Contains(stdout, "  e1  ") {
		t.Errorf("stdout = %q, want trimmed canonical name without raw padding", stdout)
	}
}

func TestEndpointCommands(t *testing.T) {
	scratchHome(t)
	if _, _, err := runCLI(t, "endpoint", "add", "e1", "http://ai"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, _, err := runCLI(t, "endpoint", "add", "e1", "http://other"); err == nil {
		t.Error("a second endpoint called e1 was accepted")
	}
	if _, _, err := runCLI(t, "endpoint", "add", "http://ai", "http://other"); err == nil {
		t.Error("an endpoint named like a URL was accepted")
	}

	stdout, _, err := runCLI(t, "endpoint", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout, "e1") || !strings.Contains(stdout, "http://ai") {
		t.Errorf("list = %q, want the named endpoint", stdout)
	}

	// e1 names the default endpoint, which is active and undeletable, so the
	// delete success path needs a second, inactive endpoint.
	if _, _, err := runCLI(t, "endpoint", "add", "e2", "http://other"); err != nil {
		t.Fatalf("add e2: %v", err)
	}
	if _, _, err := runCLI(t, "endpoint", "delete", "e2"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	stdout, _, _ = runCLI(t, "endpoint", "list")
	if strings.Contains(stdout, "e2") {
		t.Errorf("list = %q after delete, want e2 gone", stdout)
	}
	if _, _, err := runCLI(t, "endpoint", "delete", "e2"); err == nil {
		t.Error("deleting a missing endpoint did not fail")
	}
}

// The active endpoint is the connection the user falls back to, so delete
// refuses it and says to connect elsewhere first.
func TestEndpointDeleteRefusesTheActiveOne(t *testing.T) {
	scratchHome(t)
	if _, _, err := runCLI(t, "endpoint", "add", "e1", "http://ai"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, _, err := runCLI(t, "endpoint", "delete", "e1"); err == nil {
		t.Error("deleted the active endpoint")
	}
}

// A bridged endpoint names its bridge in the list, and naming the endpoint
// resolves it for the launcher flags.
func TestEndpointThroughABridge(t *testing.T) {
	scratchHome(t)
	if _, _, err := runCLI(t, "bridge", "add", "work"); err != nil {
		t.Fatalf("bridge add: %v", err)
	}
	if _, _, err := runCLI(t, "endpoint", "add", "e1", "--bridge", "work", "http://ai"); err != nil {
		t.Fatalf("endpoint add: %v", err)
	}
	stdout, _, err := runCLI(t, "endpoint", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout, "e1") || !strings.Contains(stdout, "work") {
		t.Errorf("list = %q, want e1 through work", stdout)
	}
}
