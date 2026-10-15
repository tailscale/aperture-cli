package e2e

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// releaseBinary returns the latest released aperture binary, downloading and
// caching it. The compat suite runs the released binary to produce state and
// the branch binary to consume it, so the backward-compatibility claim is
// tested against what users actually have rather than a hand-built fixture.
func releaseBinary(t *testing.T) string {
	t.Helper()
	tag := latestReleaseTag(t)
	var name string
	if runtime.GOOS == "darwin" {
		name = fmt.Sprintf("aperture_%s_darwin_%s.zip", tag, runtime.GOARCH)
	} else {
		name = fmt.Sprintf("aperture-cli_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	}

	dir := filepath.Join(userCacheDir(t), "aperture-e2e-releases", tag+"-"+runtime.GOOS+"-"+runtime.GOARCH)
	bin := filepath.Join(dir, "aperture")
	if _, err := os.Stat(bin); err == nil {
		return bin
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/tailscale/aperture-cli/releases/download/" + tag + "/" + name
	resp, err := httpClient().Get(url)
	if err != nil {
		t.Fatalf("downloading %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("downloading %s: %s", url, resp.Status)
	}
	if strings.HasSuffix(name, ".zip") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) != "aperture" {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			out, err := os.OpenFile(bin, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				rc.Close()
				t.Fatal(err)
			}
			_, err = io.Copy(out, rc)
			rc.Close()
			out.Close()
			if err != nil {
				t.Fatal(err)
			}
			return bin
		}
		t.Fatalf("no aperture binary in %s", url)
		return ""
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(hdr.Name) != "aperture" {
			continue
		}
		out, err := os.OpenFile(bin, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			t.Fatal(err)
		}
		out.Close()
		return bin
	}
	t.Fatalf("no aperture binary in %s", url)
	return ""
}

// latestReleaseTag resolves the latest release tag the way install.sh does:
// the redirect target of /releases/latest.
func latestReleaseTag(t *testing.T) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodHead, "https://github.com/tailscale/aperture-cli/releases/latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := httpClient()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("resolving the latest release: %v", err)
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if tag == "" || tag == loc {
		t.Fatalf("no release tag in redirect %q", loc)
	}
	return tag
}

func httpClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}
}

func userCacheDir(t *testing.T) string {
	t.Helper()
	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

var (
	// bridgeIDPattern matches the generated part of a bridge ID so golden
	// output can pin the shape of a table without pinning randomness.
	bridgeIDPattern = regexp.MustCompile(`bridge-[0-9a-f]{6}`)
	// detailsPattern matches the run-log path reportFailure prints after an
	// error, which carries the run's temp directory.
	detailsPattern = regexp.MustCompile(`details: .*?aperture\.log`)
)

// normalizeGolden replaces the random parts of command output with fixed
// tokens, so contract goldens stay stable across runs.
func normalizeGolden(s string) string {
	s = bridgeIDPattern.ReplaceAllString(s, "bridge-XXXXXX")
	return detailsPattern.ReplaceAllString(s, "details: <run-log>")
}
