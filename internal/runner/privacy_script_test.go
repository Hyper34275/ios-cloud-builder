package runner_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPrivacyCheckScript runs scripts/privacy-check.sh against a throwaway
// repository, so the check CI relies on cannot silently stop detecting leaks.
func TestPrivacyCheckScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the privacy check runs on the Linux CI runner and macOS")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	script, err := filepath.Abs("../../scripts/privacy-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	git := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	noreply := []string{
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=1+t@users.noreply.github.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=1+t@users.noreply.github.com",
	}
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(nil, "init", "-q", "-b", "main")
	write("clean.txt", "base\n")
	git(nil, "add", "-A")
	git(noreply, "commit", "-q", "-m", "base")
	git(nil, "branch", "base")

	run := func(denylist string) (string, bool) {
		t.Helper()
		cmd := exec.Command("bash", script, "base", "HEAD")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "PRIVACY_DENYLIST="+denylist, "GITHUB_ACTIONS=", "PR_TITLE=", "PR_BODY=")
		out, err := cmd.CombinedOutput()
		return string(out), err == nil
	}

	write("fine.txt", "/home/runner/work\n/Users/runner/x\nC:\\Users\\runneradmin\\x\nuser@example.com\n")
	git(nil, "add", "-A")
	git(noreply, "commit", "-q", "-m", "fine")
	if out, ok := run("Hidden"); !ok {
		t.Fatalf("a clean change failed:\n%s", out)
	}

	// The leaking values are assembled at run time so this file itself
	// passes the check it tests.
	home, users, mail := "/ho"+"me/", "Us"+"ers", "@gm"+"ail.com"
	write("leak.txt", strings.Join([]string{
		"cd " + home + "alice/project",
		"++ " + home + "bob/hidden-by-a-plus-prefix",
		`C:\` + users + `\Carol\code`,
		"see /" + users + "/dave",
		"mail someone" + mail,
		"the Hidden app",
		"",
	}, "\n"))
	git(nil, "add", "-A")
	git([]string{
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=person" + mail,
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=1+t@users.noreply.github.com",
	}, "commit", "-q", "-m", "mentions hidden")
	out, ok := run("Hidden")
	if ok {
		t.Fatalf("leaks passed the check:\n%s", out)
	}
	for _, want := range []string{
		"personal email; use your users.noreply.github.com address",
		"personal home directory in leak.txt:1", "personal home directory in leak.txt:2",
		"personal home directory in leak.txt:3", "personal home directory in leak.txt:4",
		"personal email address in leak.txt:5",
		"private identifier from PRIVACY_DENYLIST in leak.txt:6",
		"private identifier from PRIVACY_DENYLIST in a commit message",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "alice") || strings.Contains(out, "Hidden app") {
		t.Errorf("output repeats the matched text:\n%s", out)
	}
}
