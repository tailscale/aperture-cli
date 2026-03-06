package profiles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ClaudeCodeProfile implements Profile for the `claude` CLI tool.
type ClaudeCodeProfile struct {
	vertexProjectID string
}

func (c *ClaudeCodeProfile) Name() string { return "Claude Code" }

func (c *ClaudeCodeProfile) BinaryName() string { return "claude" }

func (c *ClaudeCodeProfile) CommonPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "bin", "claude"),
	}
}

func (c *ClaudeCodeProfile) SupportedBackends() []Backend {
	return []Backend{
		{Type: BackendAnthropic, DisplayName: "Anthropic API"},
		{Type: BackendBedrock, DisplayName: "AWS Bedrock"},
		{Type: BackendVertex, DisplayName: "Google Vertex"},
	}
}

func (c *ClaudeCodeProfile) InstallHint() string {
	return "curl -fsSL https://claude.ai/install.sh | bash"
}

func (c *ClaudeCodeProfile) UninstallHint() string {
	return "rm -f ~/.local/bin/claude && rm -rf ~/.local/share/claude"
}

func (c *ClaudeCodeProfile) Uninstall() func() error {
	return func() error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		os.Remove(filepath.Join(home, ".local", "bin", "claude"))
		return os.RemoveAll(filepath.Join(home, ".local", "share", "claude"))
	}
}

func (c *ClaudeCodeProfile) SetVertexProjectID(id string) { c.vertexProjectID = id }

func (c *ClaudeCodeProfile) YoloArgs() []string {
	return []string{"--dangerously-skip-permissions"}
}

func (c *ClaudeCodeProfile) RequiredCompat(b Backend) []string {
	switch b.Type {
	case BackendAnthropic:
		return []string{"anthropic_messages"}
	case BackendBedrock:
		return []string{"bedrock_model_invoke"}
	case BackendVertex:
		return []string{"google_raw_predict"}
	default:
		return nil
	}
}

func (c *ClaudeCodeProfile) ProviderEnv(b Backend, providers []ProviderInfo) map[string]string {
	keys := c.RequiredCompat(b)

	// Collect models from all providers compatible with the chosen backend.
	var models []string
	for _, p := range providers {
		for _, k := range keys {
			if p.Compatibility[k] {
				models = append(models, p.Models...)
				break
			}
		}
	}

	env := make(map[string]string)
	type modelVar struct {
		substr string
		envKey string
	}
	targets := []modelVar{
		{"opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"},
		{"sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
		{"haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
	}

	sort.Sort(sort.Reverse(sort.StringSlice(models)))
	for _, m := range models {
		lower := strings.ToLower(m)
		for _, t := range targets {
			if _, ok := env[t.envKey]; !ok && strings.Contains(lower, t.substr) {
				env[t.envKey] = m
			}
		}
	}
	return env
}

// managedEnvVars returns every environment variable name that the launcher
// may set when launching Claude Code, across all backends.
var managedEnvVars = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BEDROCK_BASE_URL",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_SKIP_BEDROCK_AUTH",
	"CLOUD_ML_REGION",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_SKIP_VERTEX_AUTH",
	"ANTHROPIC_VERTEX_PROJECT_ID",
	"ANTHROPIC_VERTEX_BASE_URL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
}

// Check validates that ~/.claude/settings.json does not set environment
// variables that conflict with what the launcher manages. Claude Code
// applies env from settings.json on startup, which would override the
// values the launcher injects via the process environment.
func (c *ClaudeCodeProfile) Check(b Backend) error {
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

func (c *ClaudeCodeProfile) Env(apertureHost string, b Backend) (map[string]string, error) {
	switch b.Type {
	case BackendAnthropic:
		return map[string]string{
			"ANTHROPIC_BASE_URL":   apertureHost,
			"ANTHROPIC_AUTH_TOKEN": "-",
		}, nil
	case BackendBedrock:
		return map[string]string{
			"ANTHROPIC_BEDROCK_BASE_URL":    apertureHost + "/bedrock",
			"CLAUDE_CODE_USE_BEDROCK":       "1",
			"CLAUDE_CODE_SKIP_BEDROCK_AUTH": "1",
		}, nil
	case BackendVertex:
		return map[string]string{
			"CLOUD_ML_REGION":              "global",
			"CLAUDE_CODE_USE_VERTEX":       "1",
			"CLAUDE_CODE_SKIP_VERTEX_AUTH": "1",
			"ANTHROPIC_VERTEX_PROJECT_ID":  c.vertexProjectID,
			"ANTHROPIC_VERTEX_BASE_URL":    apertureHost + "/v1",
		}, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q for Claude Code", b.Type)
	}
}
