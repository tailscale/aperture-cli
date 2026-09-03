package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "patch", current: "v1.2.3", latest: "v1.2.4", want: true},
		{name: "minor", current: "v1.2.9", latest: "v1.3.0", want: true},
		{name: "major", current: "v1.9.9", latest: "v2.0.0", want: true},
		{name: "same", current: "v1.2.3", latest: "v1.2.3", want: false},
		{name: "older", current: "v1.2.3", latest: "v1.2.2", want: false},
		{name: "development build", current: "B42", latest: "v1.2.3", want: false},
		{name: "prerelease", current: "v1.2.3-beta.1", latest: "v1.2.3", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNewer(tt.current, tt.latest); got != tt.want {
				t.Fatalf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestLatestFromURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "aperture-cli" {
			t.Errorf("User-Agent = %q", got)
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.8","html_url":"https://github.com/tailscale/aperture-cli/releases/tag/v0.0.8"}`))
	}))
	t.Cleanup(server.Close)

	release, err := latestFromURL(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "v0.0.8" {
		t.Errorf("Version = %q", release.Version)
	}
	if release.URL != "https://github.com/tailscale/aperture-cli/releases/tag/v0.0.8" {
		t.Errorf("URL = %q", release.URL)
	}
}

func TestLatestFromURLRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "status", status: http.StatusServiceUnavailable, body: `{}`},
		{name: "malformed JSON", status: http.StatusOK, body: `{`},
		{name: "invalid version", status: http.StatusOK, body: `{"tag_name":"latest"}`},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat("x", maxResponseBytes+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)

			if _, err := latestFromURL(context.Background(), server.Client(), server.URL); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
