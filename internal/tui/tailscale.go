package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"time"
)

type tailscaleStatus int

const (
	tsNotInstalled tailscaleStatus = iota
	tsNotRunning
	tsNotConnected
	tsConnected
)

// checkTailscale probes the local Tailscale installation. Overridable for tests.
var checkTailscale = defaultCheckTailscale

func defaultCheckTailscale() tailscaleStatus {
	bin := findTailscaleBinary()
	if bin == "" {
		return tsNotInstalled
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
	if err != nil {
		return tsNotRunning
	}
	return parseTailscaleStatus(out)
}

func findTailscaleBinary() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	if runtime.GOOS == "darwin" {
		const macApp = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
		if _, err := os.Stat(macApp); err == nil {
			return macApp
		}
	}
	return ""
}

func parseTailscaleStatus(data []byte) tailscaleStatus {
	var status struct {
		BackendState string `json:"BackendState"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return tsNotRunning
	}
	switch status.BackendState {
	case "Running":
		return tsConnected
	case "NeedsLogin", "NeedsMachineAuth":
		return tsNotConnected
	default:
		return tsNotRunning
	}
}
