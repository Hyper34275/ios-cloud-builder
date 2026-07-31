package snapshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateCapturesWorkingTreeWithoutTouchingRepo(t *testing.T) {
	repo := initRepo(t)

	write(t, repo, "tracked.txt", "modified")
	write(t, repo, "untracked.txt", "new")
	write(t, repo, "ignored.txt", "secret")

	statusBefore := run(t, repo, "status", "--porcelain")
	headBefore := run(t, repo, "rev-parse", "HEAD")

	sha, err := Create(context.Background(), "snapshot test")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got := run(t, repo, "show", sha+":tracked.txt"); got != "modified" {
		t.Errorf("tracked.txt = %q, want %q", got, "modified")
	}
	if got := run(t, repo, "show", sha+":untracked.txt"); got != "new" {
		t.Errorf("untracked.txt = %q, want %q", got, "new")
	}
	if _, err := exec.Command("git", "-C", repo, "show", sha+":ignored.txt").Output(); err == nil {
		t.Error("ignored.txt is in the snapshot, want it excluded by .gitignore")
	}

	if parent := run(t, repo, "rev-parse", sha+"^"); parent != headBefore {
		t.Errorf("parent = %s, want HEAD %s", parent, headBefore)
	}
	if got := run(t, repo, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD moved to %s, want %s", got, headBefore)
	}
	if got := run(t, repo, "status", "--porcelain"); got != statusBefore {
		t.Errorf("status = %q, want unchanged %q", got, statusBefore)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		run(t, dir, args...)
	}

	write(t, dir, "tracked.txt", "original")
	write(t, dir, ".gitignore", "ignored.txt\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-m", "initial")

	// Create runs git in the process working directory.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	return dir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()

	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}

	return strings.TrimSpace(string(out))
}
