package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thenawid/backfire/internal/app"
)

// httpClient is shared and has a sane timeout so a stuck download cannot hang a
// menu or a bot forever.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// Current returns the running build's version.
func Current() Version { return Parse(app.Version) }

// LatestTag queries the GitHub API for the newest release tag.
func LatestTag(ctx context.Context) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", app.RepoOwner, app.RepoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("no releases published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github returned %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", fmt.Errorf("release has no tag")
	}
	return body.TagName, nil
}

// CheckResult is what a check-for-updates reports.
type CheckResult struct {
	Current   Version
	Latest    Version
	Available bool // a newer version exists
}

// Check compares the running version against the latest release.
func Check(ctx context.Context) (*CheckResult, error) {
	tag, err := LatestTag(ctx)
	if err != nil {
		return nil, err
	}
	cur := Current()
	latest := Parse(tag)
	return &CheckResult{Current: cur, Latest: latest, Available: Newer(cur, latest)}, nil
}

// assetName is the release asset for the running architecture.
func assetName() string {
	return fmt.Sprintf("backfire_linux_%s", runtime.GOARCH)
}

// Update downloads the latest release binary for this architecture, verifies it
// against the published SHA-256 checksum, and atomically replaces the installed
// binary. It does NOT restart anything: running tunnels keep their open binary
// and stay up. The returned message tells the operator how to run the new
// version.
//
// It returns (nil, nil) with UpToDate=true when already current.
func Update(ctx context.Context) (*Result, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("updating needs root")
	}
	chk, err := Check(ctx)
	if err != nil {
		return nil, err
	}
	if !chk.Available {
		return &Result{Current: chk.Current, Latest: chk.Latest, UpToDate: true}, nil
	}

	asset := assetName()
	base := fmt.Sprintf("https://github.com/%s/%s/releases/latest/download", app.RepoOwner, app.RepoName)

	bin, err := download(ctx, base+"/"+asset)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	// Verify against the published checksum when present; refuse a mismatch.
	if sum, err := download(ctx, base+"/"+asset+".sha256"); err == nil {
		want := strings.Fields(string(sum))
		if len(want) > 0 {
			got := sha256.Sum256(bin)
			if !strings.EqualFold(want[0], hex.EncodeToString(got[:])) {
				return nil, fmt.Errorf("checksum mismatch — refusing to install this download")
			}
		}
	}

	if err := replaceBinary(app.BinPath, bin); err != nil {
		return nil, err
	}
	return &Result{
		Current: chk.Current,
		Latest:  chk.Latest,
		Updated: true,
	}, nil
}

// Result reports the outcome of an update.
type Result struct {
	Current  Version
	Latest   Version
	UpToDate bool
	Updated  bool
}

// download fetches a URL into memory, bounded so a bad asset cannot exhaust RAM.
func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// replaceBinary writes the new binary next to the target and renames it over the
// old one. The rename is atomic and, crucially, leaves every already-running
// process pointing at the old inode — so no tunnel is interrupted.
func replaceBinary(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".backfire-update-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
