package runner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// RevokeInstallationToken invalidates the narrowly scoped GitHub App token.
// The token is intentionally accepted by the Go API (and environment by the
// CLI) so it never appears in a command line.
func RevokeInstallationToken(ctx context.Context, token string) error {
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return fmt.Errorf("invalid installation token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "https://api.github.com/installation/token", nil)
	if err != nil {
		return fmt.Errorf("create revocation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("revoke installation token")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("revoke installation token: GitHub returned status %d", resp.StatusCode)
	}
	return nil
}

var credentialConfigPattern = regexp.MustCompile(`(?i)(extraheader|authorization\s*:|x-access-token|ghs_[a-z0-9]|github_pat_|https?://[^\s/@]+@)`)

// VerifyCheckoutNoCredentials fails closed if actions/checkout or Git left an
// HTTP authorization header or URL credential in the private checkout.
func VerifyCheckoutNoCredentials(sourceRoot string) error {
	gitPath := filepath.Join(sourceRoot, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return fmt.Errorf("private checkout has no .git metadata")
	}
	gitDir := gitPath
	if info.Mode().IsRegular() {
		contents, readErr := os.ReadFile(gitPath)
		if readErr != nil {
			return fmt.Errorf("read private checkout metadata")
		}
		line := strings.TrimSpace(string(contents))
		if !strings.HasPrefix(line, "gitdir: ") {
			return fmt.Errorf("invalid private checkout metadata")
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(sourceRoot, gitDir)
		}
	}
	for _, name := range []string{"config", "config.worktree"} {
		contents, readErr := os.ReadFile(filepath.Join(gitDir, name))
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return fmt.Errorf("read private checkout git configuration")
		}
		if credentialConfigPattern.Match(contents) {
			return fmt.Errorf("private checkout contains persisted credentials")
		}
	}
	return nil
}
