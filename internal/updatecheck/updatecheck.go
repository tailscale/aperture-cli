// Package updatecheck reports when a newer stable Aperture CLI release is available.
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const (
	latestReleaseURL = "https://api.github.com/repos/tailscale/aperture-cli/releases/latest"
	maxResponseBytes = 1 << 20
)

var stableVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Release describes the latest stable GitHub release.
type Release struct {
	Version string
	URL     string
}

// Latest fetches the latest stable Aperture CLI release from GitHub.
func Latest(ctx context.Context, client *http.Client) (Release, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return latestFromURL(ctx, client, latestReleaseURL)
}

func latestFromURL(ctx context.Context, client *http.Client, endpoint string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "aperture-cli")

	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("latest release request returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Release{}, err
	}
	if len(body) > maxResponseBytes {
		return Release{}, fmt.Errorf("latest release response exceeds %d bytes", maxResponseBytes)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Release{}, fmt.Errorf("decode latest release response: %w", err)
	}

	release := Release{
		Version: strings.TrimSpace(payload.TagName),
		URL:     strings.TrimSpace(payload.HTMLURL),
	}
	if !ValidVersion(release.Version) {
		return Release{}, fmt.Errorf("latest release has invalid version %q", release.Version)
	}
	return release, nil
}

// ValidVersion reports whether version is a stable three component semantic version.
func ValidVersion(version string) bool {
	return stableVersionPattern.MatchString(version)
}

// IsNewer reports whether latest is a newer stable version than current.
func IsNewer(current, latest string) bool {
	currentParts, ok := versionParts(current)
	if !ok {
		return false
	}
	latestParts, ok := versionParts(latest)
	if !ok {
		return false
	}
	for i := range currentParts {
		if latestParts[i] != currentParts[i] {
			return latestParts[i] > currentParts[i]
		}
	}
	return false
}

func versionParts(version string) ([3]uint64, bool) {
	match := stableVersionPattern.FindStringSubmatch(version)
	if match == nil {
		return [3]uint64{}, false
	}
	var parts [3]uint64
	for i := range parts {
		part, err := strconv.ParseUint(match[i+1], 10, 64)
		if err != nil {
			return [3]uint64{}, false
		}
		parts[i] = part
	}
	return parts, true
}
