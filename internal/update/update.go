// Package update self-updates the builder binary from GitHub Releases.
package update

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
	"strconv"
	"strings"
	"time"
)

const (
	owner = "MobAI-App"
	repo  = "ios-builder"
)

type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Run checks GitHub for a newer release and, if there is one, replaces the
// running binary with it. currentVersion is what this binary was built with
// ("dev" for an unversioned local build, which always updates).
func Run(ctx context.Context, currentVersion string) error {
	rel, err := latestRelease(ctx)
	if err != nil {
		return err
	}

	if !isNewer(rel.TagName, currentVersion) {
		fmt.Printf("Already up to date (%s).\n", display(currentVersion))
		return nil
	}
	fmt.Printf("Updating %s -> %s\n", display(currentVersion), rel.TagName)

	asset := assetName()
	binURL, ok := assetURL(rel, asset)
	if !ok {
		return fmt.Errorf("release %s has no binary for %s/%s", rel.TagName, runtime.GOOS, runtime.GOARCH)
	}
	sumsURL, ok := assetURL(rel, "checksums.txt")
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt", rel.TagName)
	}

	binary, err := fetch(ctx, binURL)
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	sums, err := fetch(ctx, sumsURL)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}

	want, ok := checksumFor(string(sums), asset)
	if !ok {
		return fmt.Errorf("no checksum for %s", asset)
	}
	if got := sha256.Sum256(binary); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s - not replacing the binary", asset)
	}

	if err := replaceExecutable(binary); err != nil {
		return err
	}
	fmt.Printf("Updated to %s.\n", rel.TagName)
	return nil
}

func latestRelease(ctx context.Context) (*release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	body, err := fetch(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("check latest release: %w", err)
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("parse release: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("no release found")
	}
	return &rel, nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// assetName is the release asset for the current platform, matching the names
// the release workflow uploads (builder-<os>-<arch>, .exe on Windows).
func assetName() string {
	name := fmt.Sprintf("builder-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func assetURL(rel *release, name string) (string, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a.URL, true
		}
	}
	return "", false
}

// checksumFor pulls one file's hash out of a `<sha256>  <name>` checksums file.
func checksumFor(sums, name string) (string, bool) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0], true
		}
	}
	return "", false
}

// replaceExecutable writes newBin over the running binary.
//
// On Unix the path is renamed atomically while the old inode stays live for the
// current process. Windows cannot rename over a running .exe, so the old one is
// moved aside first and cleaned up on the next run.
func replaceExecutable(newBin []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".builder-update-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s (a package-managed install may need its own upgrade command): %w", exe, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename below succeeds

	if _, err := tmp.Write(newBin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0755); err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmpName, exe); err != nil {
			_ = os.Rename(old, exe) // roll back
			return err
		}
		_ = os.Remove(old) // often locked while running; harmless to leave
		return nil
	}
	return os.Rename(tmpName, exe)
}

// isNewer reports whether tag (vX.Y.Z) is a higher version than current. A "dev"
// or unparseable current always updates; equal or lower versions do not.
func isNewer(tag, current string) bool {
	if current == "" || current == "dev" {
		return true
	}
	t, ok1 := parseVersion(tag)
	c, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return tag != current
	}
	for i := 0; i < 3; i++ {
		if t[i] != c[i] {
			return t[i] > c[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func display(v string) string {
	if v == "" || v == "dev" {
		return "dev build"
	}
	return v
}
