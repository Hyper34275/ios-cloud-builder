// Package snapshot captures the working tree as a Git commit and publishes it
// to a remote ref, leaving HEAD, the current branch and the index untouched.
package snapshot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// refPrefix is the remote ref namespace holding build snapshots. Refs outside
// refs/heads and refs/tags do not appear as branches and fire no push events.
const refPrefix = "refs/ios-builder/jobs/"

// Ref returns the remote ref that holds the snapshot for a build.
func Ref(buildID string) string {
	return refPrefix + buildID
}

// Create commits the working tree, including staged, unstaged and untracked
// files, and returns the commit SHA. Files excluded by .gitignore are not
// included. The commit's parent is HEAD, so it is never reachable from any
// local branch.
func Create(ctx context.Context, message string) (string, error) {
	dir, err := os.MkdirTemp("", "ios-builder-index")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary index: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	index := filepath.Join(dir, "index")
	if _, err := git(ctx, index, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := git(ctx, index, "add", "-A"); err != nil {
		return "", err
	}

	tree, err := git(ctx, index, "write-tree")
	if err != nil {
		return "", err
	}

	return git(ctx, index, "commit-tree", tree, "-p", "HEAD", "-m", message)
}

// Push publishes a snapshot commit to ref on the remote.
func Push(ctx context.Context, remote, sha, ref string) error {
	_, err := git(ctx, "", "push", remote, sha+":"+ref)
	return err
}

// Delete removes ref from the remote.
func Delete(ctx context.Context, remote, ref string) error {
	_, err := git(ctx, "", "push", "--delete", remote, ref)
	return err
}

func git(ctx context.Context, index string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	// Without this, a repository using HTTPS without a credential helper blocks
	// on an interactive password prompt instead of failing.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}

	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}

	return strings.TrimSpace(string(out)), nil
}
