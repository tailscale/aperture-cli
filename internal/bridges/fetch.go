package bridges

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tailscale/aperture-cli/internal/config"
)

const (
	providerFetchTimeout       = 10 * time.Second
	bridgeProviderFetchTimeout = 30 * time.Second
)

// fetchProviders asks the Aperture at host what it serves. This request is
// the attempt's AskingForModels phase, and its success is what verifies the
// connection.
func fetchProviders(ctx context.Context, host string, timeout time.Duration) ([]config.ProviderInfo, error) {
	client := &http.Client{Timeout: timeout}
	// Append to the path, not the string. ParseEndpointURL accepts a query,
	// and appending to "host?token=x" put the models path inside the query.
	base, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	target := base.JoinPath("v1", "models").String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	// Aperture intentionally filters model results for Claude Code user agents.
	// Discovery needs the full grant-filtered model list for every harness.
	req.Header.Set("User-Agent", "aperture-cli")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return nil, fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, target, detail)
		}
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, target)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	provs, err := config.ParseProviders(body)
	if err != nil {
		return nil, fmt.Errorf("could not parse models response: %w", err)
	}
	return provs, nil
}
