package profiles

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// OpenCodeProfile implements Profile for the `opencode` CLI tool.
type OpenCodeProfile struct {
}

func (o *OpenCodeProfile) Name() string { return "OpenCode" }

func (o *OpenCodeProfile) BinaryName() string { return "opencode" }

func (o *OpenCodeProfile) CommonPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".opencode", "bin", "opencode"),
		filepath.Join(home, ".local", "bin", "opencode"),
	}
}

func (o *OpenCodeProfile) InstallHint() string {
	return "curl -fsSL https://opencode.ai/install | bash"
}

func (o *OpenCodeProfile) UninstallHint() string {
	return "opencode uninstall --force\nrm -rf ~/.opencode/bin"
}

func (o *OpenCodeProfile) Uninstall() func() error {
	return func() error {
		if err := exec.Command("opencode", "uninstall", "--force").Run(); err != nil {
			return err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return os.RemoveAll(filepath.Join(home, ".opencode", "bin"))
	}
}

func (o *OpenCodeProfile) SupportedBackends() []Backend {
	return []Backend{
		{Type: BackendAnthropic, DisplayName: "Anthropic API"},
		{Type: BackendBedrock, DisplayName: "AWS Bedrock"},
		{Type: BackendVertex, DisplayName: "Google Vertex"},
		{Type: BackendOpenAI, DisplayName: "OpenAI Compatible"},
	}
}

func (o *OpenCodeProfile) RequiredCompat(b Backend) []string {
	switch b.Type {
	case BackendAnthropic:
		return []string{"anthropic_messages"}
	case BackendBedrock:
		return []string{"bedrock_converse"}
	case BackendVertex:
		return []string{"google_generate_content"}
	case BackendOpenAI:
		return []string{"openai_chat"}
	default:
		return nil
	}
}

func (o *OpenCodeProfile) Env(apertureHost string, b Backend) (map[string]string, error) {
	switch b.Type {
	case BackendAnthropic:
		return map[string]string{
			"ANTHROPIC_BASE_URL":   apertureHost + "/bedrock",
			"ANTHROPIC_AUTH_TOKEN": "-",
		}, nil
	case BackendBedrock:
		// Dummy AWS credentials so the SDK doesn't fail credential resolution;
		// aperture handles actual auth at the /bedrock endpoint.
		return map[string]string{
			"AWS_ACCESS_KEY_ID":     "not-needed",
			"AWS_SECRET_ACCESS_KEY": "not-needed",
			"AWS_REGION":            "us-east-1",
		}, nil
	case BackendVertex:
		return map[string]string{
			"GOOGLE_CLOUD_PROJECT":  "_aperture_auto_vertex_project_id_",
			"GOOGLE_CLOUD_LOCATION": "_aperture_auto_vertex_region_",
		}, nil
	case BackendOpenAI:
		return map[string]string{
			"OPENAI_API_KEY":  "not-needed",
			"OPENAI_BASE_URL": apertureHost + "/v1",
		}, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q for OpenCode", b.Type)
	}
}

type opencodeConfig struct {
	Schema   string                      `json:"$schema,omitempty"`
	Provider map[string]opencodeProvider `json:"provider,omitempty"`
}

type opencodeProvider struct {
	Options map[string]string `json:"options,omitempty"`
}

func (o *OpenCodeProfile) WriteConfig(apertureHost string, b Backend) (string, string, func(), error) {
	var providerKey string
	var options map[string]string

	switch b.Type {
	case BackendAnthropic:
		providerKey = "anthropic"
		options = map[string]string{
			"apiKey":  "{env:ANTHROPIC_AUTH_TOKEN}",
			"baseURL": "{env:ANTHROPIC_BASE_URL}",
		}
	case BackendBedrock:
		providerKey = "amazon-bedrock"
		options = map[string]string{
			"region":   "us-east-1",
			"endpoint": apertureHost + "/bedrock",
		}
	case BackendVertex:
		providerKey = "google-vertex"
		options = map[string]string{
			"project":  "_aperture_auto_vertex_project_id_",
			"location": "_aperture_auto_vertex_region_",
			"baseURL":  apertureHost + "/v1",
		}
	case BackendOpenAI:
		providerKey = "openai"
		options = map[string]string{
			"apiKey":  "{env:OPENAI_API_KEY}",
			"baseURL": "{env:OPENAI_BASE_URL}",
		}
	default:
		return "", "", nil, fmt.Errorf("unsupported backend %q for OpenCode", b.Type)
	}

	provider := opencodeProvider{Options: options}

	cfg := opencodeConfig{
		Schema: "https://opencode.ai/config.json",
		Provider: map[string]opencodeProvider{
			providerKey: provider,
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		return "", "", nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", nil, err
	}
	configDir := filepath.Join(home, ".opencode")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", "", nil, err
	}
	path := filepath.Join(configDir, "tmp_aperture_config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", nil, err
	}
	return "OPENCODE_CONFIG", path, func() { os.Remove(path) }, nil
}
