// Package snapshot exposes working-tree snapshotting to code outside this module.
//
// Push takes the target ref as a parameter, so callers are not limited to the
// namespace Ref returns.
package snapshot

import (
	"context"
	"time"

	"github.com/MobAI-App/ios-builder/internal/snapshot"
)

const DefaultMaxAge = snapshot.DefaultMaxAge

type RemoteRef = snapshot.RemoteRef

// Ref returns the remote ref that holds the snapshot for a build.
func Ref(buildID string) string {
	return snapshot.Ref(buildID)
}

// Create commits the working tree, including staged, unstaged and untracked
// files, and returns the commit SHA. HEAD, the current branch and the index are
// left untouched.
func Create(ctx context.Context, message string) (string, error) {
	return snapshot.Create(ctx, message)
}

// Push publishes a snapshot commit to ref on the remote.
func Push(ctx context.Context, remote, sha, ref string) error {
	return snapshot.Push(ctx, remote, sha, ref)
}

// Delete removes ref from the remote.
func Delete(ctx context.Context, remote, ref string) error {
	return snapshot.Delete(ctx, remote, ref)
}

func DeleteLease(ctx context.Context, remote, ref, expectedSHA string) error {
	return snapshot.DeleteLease(ctx, remote, ref, expectedSHA)
}

func VerifyRemote(ctx context.Context, remote, owner, repo string) error {
	return snapshot.VerifyRemote(ctx, remote, owner, repo)
}

func ListStale(ctx context.Context, remote string, maxAge time.Duration, now time.Time) ([]RemoteRef, error) {
	return snapshot.ListStale(ctx, remote, maxAge, now)
}

func Cleanup(ctx context.Context, remote string, maxAge time.Duration, now time.Time) ([]RemoteRef, error) {
	return snapshot.Cleanup(ctx, remote, maxAge, now)
}
