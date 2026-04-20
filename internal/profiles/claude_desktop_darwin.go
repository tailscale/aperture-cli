package profiles

import (
	"fmt"
	"os/exec"
	"strings"
)

func platformBinaryName() string { return "Claude" }

func platformCommonPaths() []string {
	return []string{"/Applications/Claude.app/Contents/MacOS/Claude"}
}

func platformInstallHint() string {
	return "Opens the Claude download page in your browser.\nInstall the app, then come back here to launch it."
}

func platformConfigure(baseURL string) error {
	domain := "com.anthropic.claude"
	entries := [][2]string{
		{"inferenceProvider", "gateway"},
		{"inferenceGatewayApiKey", "-"},
		{"inferenceGatewayBaseUrl", baseURL},
	}
	for _, e := range entries {
		if err := exec.Command("defaults", "write", domain, e[0], "-string", e[1]).Run(); err != nil {
			return fmt.Errorf("defaults write %s: %w", e[0], err)
		}
	}
	return nil
}

func platformReadGatewayURL() string {
	out, err := exec.Command("defaults", "read", "com.anthropic.claude", "inferenceGatewayBaseUrl").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func platformInstallCmd() *exec.Cmd {
	return exec.Command("open", "https://claude.ai/api/desktop/darwin/universal/dmg/latest/redirect?utm_source=aperture_cli")
}

func platformLaunch() error {
	return exec.Command("open", "-a", "Claude").Run()
}
