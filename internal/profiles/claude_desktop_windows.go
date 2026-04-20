package profiles

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func platformBinaryName() string { return "Claude.exe" }

func platformCommonPaths() []string {
	// MSIX install: query the package install location via PowerShell.
	out, err := exec.Command("powershell", "-Command",
		`(Get-AppxPackage -Name "Claude" | Select-Object -First 1).InstallLocation`).Output()
	if err == nil {
		loc := strings.TrimSpace(string(out))
		if loc != "" {
			p := filepath.Join(loc, "app", "Claude.exe")
			if _, err := os.Stat(p); err == nil {
				return []string{p}
			}
		}
	}

	// Squirrel install fallback.
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return nil
	}
	return []string{
		filepath.Join(localAppData, "Programs", "claude-desktop", "Claude.exe"),
	}
}

func platformInstallHint() string {
	return "Opens the Claude download page in your browser.\nInstall the app, then come back here to launch it."
}

func platformConfigure(baseURL string) error {
	regPath := `HKCU\SOFTWARE\Policies\Claude`
	entries := [][2]string{
		{"inferenceProvider", "gateway"},
		{"inferenceGatewayApiKey", "-"},
		{"inferenceGatewayBaseUrl", baseURL},
	}
	for _, e := range entries {
		cmd := exec.Command("reg", "add", regPath, "/v", e[0], "/t", "REG_SZ", "/d", e[1], "/f")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("reg add %s: %w", e[0], err)
		}
	}
	return nil
}

func platformReadGatewayURL() string {
	out, err := exec.Command("reg", "query", `HKCU\SOFTWARE\Policies\Claude`, "/v", "inferenceGatewayBaseUrl").Output()
	if err != nil {
		return ""
	}
	// reg query output: "    inferenceGatewayBaseUrl    REG_SZ    https://..."
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "inferenceGatewayBaseUrl") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				return parts[len(parts)-1]
			}
		}
	}
	return ""
}

func platformInstallCmd() *exec.Cmd {
	url := "https://claude.ai/api/desktop/win32/x64/setup/latest/redirect?utm_source=aperture_cli"
	if runtime.GOARCH == "arm64" {
		url = "https://claude.ai/api/desktop/win32/arm64/setup/latest/redirect?utm_source=aperture_cli"
	}
	return exec.Command("cmd", "/c", "start", "", url)
}

func platformLaunch() error {
	return exec.Command("cmd", "/c", "start", "", "claude://").Run()
}
