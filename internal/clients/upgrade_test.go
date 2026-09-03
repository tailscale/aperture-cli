package clients

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestDetectInstallMethod(t *testing.T) {
	t.Run("npm symlink into node_modules", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "lib", "node_modules", "@openai", "codex", "bin", "codex.js")
		touch(t, real)
		bin := filepath.Join(dir, "bin", "codex")
		symlink(t, real, bin)
		if got := DetectInstallMethod(bin, "@openai/codex"); got != MethodNPM {
			t.Errorf("method = %v, want MethodNPM", got)
		}
	})

	t.Run("npm sibling node_modules (Windows prefix layout)", func(t *testing.T) {
		dir := t.TempDir()
		touch(t, filepath.Join(dir, "node_modules", "@openai", "codex", "package.json"))
		bin := filepath.Join(dir, "codex")
		touch(t, bin)
		if got := DetectInstallMethod(bin, "@openai/codex"); got != MethodNPM {
			t.Errorf("method = %v, want MethodNPM", got)
		}
	})

	t.Run("npm ../lib/node_modules (Unix prefix layout)", func(t *testing.T) {
		dir := t.TempDir()
		touch(t, filepath.Join(dir, "lib", "node_modules", "@google", "gemini-cli", "package.json"))
		bin := filepath.Join(dir, "bin", "gemini")
		touch(t, bin)
		if got := DetectInstallMethod(bin, "@google/gemini-cli"); got != MethodNPM {
			t.Errorf("method = %v, want MethodNPM", got)
		}
	})

	t.Run("homebrew cellar symlink", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "Cellar", "gemini-cli", "1.0.0", "bin", "gemini")
		touch(t, real)
		bin := filepath.Join(dir, "bin", "gemini")
		symlink(t, real, bin)
		if got := DetectInstallMethod(bin, "@google/gemini-cli"); got != MethodHomebrew {
			t.Errorf("method = %v, want MethodHomebrew", got)
		}
	})

	t.Run("npm wins over brew-owned node prefix", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(dir, "homebrew", "lib", "node_modules", "@openai", "codex", "bin", "codex.js")
		touch(t, real)
		bin := filepath.Join(dir, "homebrew", "bin", "codex")
		symlink(t, real, bin)
		if got := DetectInstallMethod(bin, "@openai/codex"); got != MethodNPM {
			t.Errorf("method = %v, want MethodNPM", got)
		}
	})

	t.Run("native install", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, ".local", "bin", "claude")
		touch(t, bin)
		if got := DetectInstallMethod(bin, "@anthropic-ai/claude-code"); got != MethodNative {
			t.Errorf("method = %v, want MethodNative", got)
		}
	})
}

func TestUpgradeCommand(t *testing.T) {
	spec := UpgradeSpec{
		BinaryName:     "claude",
		NPMPackage:     "@anthropic-ai/claude-code",
		BrewName:       "claude-code",
		SelfUpdateArgs: []string{"update"},
	}
	tests := []struct {
		name   string
		spec   UpgradeSpec
		method InstallMethod
		want   string
	}{
		{"npm", spec, MethodNPM, "npm install -g @anthropic-ai/claude-code@latest"},
		{"homebrew", spec, MethodHomebrew, "brew upgrade claude-code"},
		{"native uses self updater", spec, MethodNative, "claude update"},
		{
			"brew detected but no formula falls back to self updater",
			UpgradeSpec{BinaryName: "x", SelfUpdateArgs: []string{"upgrade"}},
			MethodHomebrew,
			"x upgrade",
		},
		{
			"native without self updater uses installer command",
			UpgradeSpec{BinaryName: "codex", NPMPackage: "@openai/codex", NativeUpgradeCommand: "curl -fsSL https://example.com/install.sh | sh"},
			MethodNative,
			"curl -fsSL https://example.com/install.sh | sh",
		},
		{
			"native without self updater falls back to npm",
			UpgradeSpec{BinaryName: "codex", NPMPackage: "@openai/codex"},
			MethodNative,
			"npm install -g @openai/codex@latest",
		},
		{"no paths at all", UpgradeSpec{BinaryName: "x"}, MethodNative, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upgradeCommand(tt.spec, tt.spec.BinaryName, tt.method); got != tt.want {
				t.Errorf("upgradeCommand = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBrewPackageName(t *testing.T) {
	tests := []struct {
		resolved string
		want     string
	}{
		{"/opt/homebrew/Cellar/gemini-cli/1.0.0/bin/gemini", "gemini-cli"},
		{"/opt/homebrew/Caskroom/claude-code@latest/2.1.237/claude", "claude-code@latest"},
		{"/opt/homebrew/Caskroom/copilot-cli/1.0.80/copilot-darwin-arm64/copilot", "copilot-cli"},
		{"/home/linuxbrew/.linuxbrew/Cellar/opencode/1.2.3/bin/opencode", "opencode"},
		{"/usr/local/bin/claude", ""},
	}
	for _, tt := range tests {
		if got := brewPackageName(tt.resolved); got != tt.want {
			t.Errorf("brewPackageName(%q) = %q, want %q", tt.resolved, got, tt.want)
		}
	}
}

func TestPlanUpgrade_BinaryMissing(t *testing.T) {
	plan := PlanUpgrade(UpgradeSpec{
		BinaryName: "definitely-not-a-real-binary-aperture-test",
		NPMPackage: "nope",
	})
	if plan.Run == nil {
		t.Fatal("Run is nil")
	}
	if _, err := plan.Run(); err == nil {
		t.Error("Run() error = nil, want not-found error")
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/usr/local/bin/claude"); got != "/usr/local/bin/claude" {
		t.Errorf("plain path quoted: %q", got)
	}
	if got := shellQuote("/Users/x y/bin/claude"); got != "'/Users/x y/bin/claude'" {
		t.Errorf("spaced path = %q", got)
	}
	if got := shellQuote("/a/it's/bin"); got != `'/a/it'\''s/bin'` {
		t.Errorf("single-quote path = %q", got)
	}
}
