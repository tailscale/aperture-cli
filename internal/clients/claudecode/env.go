package claudecode

import (
	"fmt"
)

// envForBackend returns the environment variables that route Claude Code
// through the aperture gateway for the chosen backend.
func envForBackend(apertureHost string, b backend) (map[string]string, error) {
	switch b.id {
	case "anthropic":
		return map[string]string{
			"ANTHROPIC_BASE_URL":   apertureHost,
			"ANTHROPIC_AUTH_TOKEN": "-",
		}, nil
	case "bedrock":
		return map[string]string{
			"ANTHROPIC_BEDROCK_BASE_URL":    apertureHost + "/bedrock",
			"CLAUDE_CODE_USE_BEDROCK":       "1",
			"CLAUDE_CODE_SKIP_BEDROCK_AUTH": "1",
		}, nil
	case "vertex":
		return map[string]string{
			"CLOUD_ML_REGION":              "_aperture_auto_vertex_region_",
			"CLAUDE_CODE_USE_VERTEX":       "1",
			"CLAUDE_CODE_SKIP_VERTEX_AUTH": "1",
			"ANTHROPIC_VERTEX_PROJECT_ID":  "_aperture_auto_vertex_project_id_",
			"ANTHROPIC_VERTEX_BASE_URL":    apertureHost + "/v1",
		}, nil
	case "zai":
		return map[string]string{
			"ANTHROPIC_BASE_URL":             apertureHost,
			"ANTHROPIC_MODEL":                "glm-5.1",
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   "glm-5.1",
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "glm-5.1",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "glm-5-turbo",
			"API_TIMEOUT_MS":                 "3000000",
			"ANTHROPIC_API_KEY":              "-",
		}, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q for Claude Code", b.id)
	}
}

// managedEnvVars is every environment variable name the launcher may set
// for Claude Code. Check() uses this list to warn when the user's
// ~/.claude/settings.json would override them.
var managedEnvVars = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_MODEL",
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
	"API_TIMEOUT_MS",
	"ANTHROPIC_API_KEY",
}
