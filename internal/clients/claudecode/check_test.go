package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck_NoSettingsFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := checkClaudeSettings(); err != nil {
		t.Fatalf("Check returned error when settings.json missing: %v", err)
	}
}

func TestCheck_NoConflicts(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"env":{"SOME_UNRELATED_VAR":"hello"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkClaudeSettings(); err != nil {
		t.Fatalf("Check returned error with no conflicting vars: %v", err)
	}
}

func TestCheck_WithConflicts(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"env":{"ANTHROPIC_BASE_URL":"https://example.com","CLAUDE_CODE_USE_BEDROCK":"1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkClaudeSettings()
	if err == nil {
		t.Fatal("Check returned nil, expected error for conflicting vars")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ANTHROPIC_BASE_URL") {
		t.Errorf("error should mention ANTHROPIC_BASE_URL, got: %s", msg)
	}
	if !strings.Contains(msg, "CLAUDE_CODE_USE_BEDROCK") {
		t.Errorf("error should mention CLAUDE_CODE_USE_BEDROCK, got: %s", msg)
	}
}

func TestCheck_InvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("{not json}"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkClaudeSettings()
	if err == nil {
		t.Fatal("Check returned nil, expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention invalid JSON, got: %s", err.Error())
	}
}

func TestCheck_EmptyEnv(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"env":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkClaudeSettings(); err != nil {
		t.Fatalf("Check returned error with empty env: %v", err)
	}
}
