package codex

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/tailscale/aperture-cli/internal/config"
)

const testHost = "http://ai.example.com"

func TestCompatibleProviders(t *testing.T) {
	provs := []config.ProviderInfo{
		{ID: "openai", SupportedEndpoints: map[string]bool{config.EndpointOpenAIResponses: true}},
		{ID: "openrouter", SupportedEndpoints: map[string]bool{config.EndpointOpenAIChat: true}},
		{ID: "anthropic", SupportedEndpoints: map[string]bool{config.EndpointAnthropicMessages: true}},
	}
	got := compatibleProviders(provs)
	if len(got) != 1 || got[0].ID != "openai" {
		t.Errorf("compatibleProviders = %+v, want [openai]", got)
	}
}

func TestFqnModels(t *testing.T) {
	p := config.ProviderInfo{ID: "openai", Models: []string{"gpt-5", "gpt-5-mini"}}
	got := fqnModels(p)
	want := []string{"openai/gpt-5", "openai/gpt-5-mini"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("fqnModels = %v, want %v", got, want)
	}
}

func TestApertureLaunchConfig(t *testing.T) {
	args, env := apertureLaunchConfig(testHost, "")
	wantArgs := []string{
		"--config", `model_provider="tailscale_aperture_cli"`,
		"--config", `model_providers.tailscale_aperture_cli={ name = "Aperture", base_url = "http://ai.example.com/v1", env_key = "APERTURE_CODEX_API_KEY", supports_websockets = false }`,
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %#v, want %#v", args, wantArgs)
	}
	wantEnv := map[string]string{apertureAPIKeyEnv: "not-needed"}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Errorf("env = %#v, want %#v", env, wantEnv)
	}
	for _, key := range []string{"CODEX_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL"} {
		if _, ok := env[key]; ok {
			t.Errorf("env unexpectedly overrides %s", key)
		}
	}
}

func TestApertureLaunchConfigWithModelCatalog(t *testing.T) {
	args, _ := apertureLaunchConfig(testHost, `/tmp/aperture "models".json`)
	want := `model_catalog_json="/tmp/aperture \"models\".json"`
	if got := args[len(args)-1]; got != want {
		t.Errorf("model catalog override = %q, want %q", got, want)
	}
}

func TestInstallPlan(t *testing.T) {
	const command = "curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh"
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			plan := installPlan(goos)
			if plan.Hint != command {
				t.Errorf("Hint = %q, want %q", plan.Hint, command)
			}
			cmd, err := plan.Run()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cmd.Args, []string{"/bin/sh", "-c", command}) {
				t.Errorf("install command args = %q, want the documented POSIX shell command", cmd.Args)
			}
		})
	}

	t.Run("other", func(t *testing.T) {
		plan := installPlan("windows")
		if plan.Hint != "npm install -g @openai/codex" {
			t.Errorf("Hint = %q, want npm fallback", plan.Hint)
		}
	})
}

func TestUninstallFallsBackToNPM(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Setenv("HOME", t.TempDir())
	}
	t.Setenv("PATH", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_INSTALL_DIR", "")

	uninstall := (&Client{}).Uninstall()
	if uninstall.Hint != "npm uninstall -g @openai/codex" {
		t.Errorf("Uninstall.Hint = %q", uninstall.Hint)
	}
}

func TestStandaloneUninstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}

	home := t.TempDir()
	codexHome := filepath.Join(home, "codex-home")
	root := filepath.Join(codexHome, "packages", "standalone")
	releaseBin := filepath.Join(root, "releases", "1.2.3", "bin")
	if err := os.MkdirAll(releaseBin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseBin, "codex"), []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseBin, "codex-code-mode-host"), []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "releases", "1.2.3"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}

	installDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(installDir, "codex")
	if err := os.Symlink(filepath.Join(root, "current", "bin", "codex"), binaryPath); err != nil {
		t.Fatal(err)
	}
	codeModeHost := filepath.Join(installDir, "codex-code-mode-host")
	if err := os.Symlink(filepath.Join(root, "current", "bin", "codex-code-mode-host"), codeModeHost); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(configPath, []byte("model = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CODEX_INSTALL_DIR", installDir)

	uninstall := (&Client{}).Uninstall()
	if !strings.Contains(uninstall.Hint, "standalone Codex installation") {
		t.Fatalf("Uninstall.Hint = %q, want standalone installer", uninstall.Hint)
	}
	if err := uninstall.Run(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{binaryPath, codeModeHost, root} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists after uninstall: %v", path, err)
		}
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("uninstall removed user configuration: %v", err)
	}
}

func TestStandaloneUninstallRejectsChangedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}

	dir := t.TempDir()
	root := filepath.Join(dir, "codex-home", "packages", "standalone")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(dir, "codex")
	if err := os.Symlink(filepath.Join(root, "current", "bin", "codex"), binaryPath); err != nil {
		t.Fatal(err)
	}
	install := standaloneInstall{binaryPath: binaryPath, root: root}
	if err := os.Remove(binaryPath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "user-managed-codex")
	if err := os.WriteFile(outside, []byte("keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, binaryPath); err != nil {
		t.Fatal(err)
	}

	err := install.remove()
	if err == nil || !strings.Contains(err.Error(), "no longer a standalone installer symlink") {
		t.Fatalf("remove error = %v, want changed-symlink error", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("user-managed binary was removed: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("standalone package root was removed after validation failed: %v", err)
	}
}

func TestReplay_StaleProvider(t *testing.T) {
	c := &Client{}
	g := &config.Global{
		LastLaunch: config.LaunchState{
			LastClientName: name,
			LastProviderID: "missing",
		},
	}
	// Binary not installed → nil regardless of provider presence.
	if cmd := c.Replay(g); cmd != nil {
		t.Error("Replay with missing binary should return nil")
	}
}
