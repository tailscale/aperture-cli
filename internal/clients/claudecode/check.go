package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// checkClaudeSettings validates that ~/.claude/settings.json does not set
// environment variables that conflict with what the launcher manages.
// Claude Code applies env from settings.json at startup, which would
// override the values the launcher injects via the process environment.
func checkClaudeSettings() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read %s\n\nCheck file permissions and try again", settingsPath)
	}

	var settings struct {
		Env map[string]any `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("%s contains invalid JSON\n\nFix the syntax or delete the file and let Claude Code recreate it", settingsPath)
	}
	if len(settings.Env) == 0 {
		return nil
	}

	var conflicts []string
	for _, key := range managedEnvVars {
		if _, ok := settings.Env[key]; ok {
			conflicts = append(conflicts, key)
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	return fmt.Errorf(
		"~/.claude/settings.json sets env vars that conflict with the launcher:\n\n  %s\n\n"+
			"The launcher manages these variables automatically.\n"+
			"Remove them from the \"env\" section of ~/.claude/settings.json",
		strings.Join(conflicts, "\n  "),
	)
}
