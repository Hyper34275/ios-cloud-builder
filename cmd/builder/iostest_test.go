package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/build"
)

func TestResolveTestScript(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "scripts", "test.sh"), "exit 0\n")
	writeFile(t, filepath.Join(repo, "ios", "ci.sh"), "exit 0\n")
	if err := os.MkdirAll(filepath.Join(repo, "scripts", "folder.sh"), 0755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, flag, config, want, wantErr string
	}{
		{name: "config", config: "scripts/test.sh", want: "scripts/test.sh"},
		{name: "flag overrides config", flag: "ios/ci.sh", config: "scripts/test.sh", want: "ios/ci.sh"},
		{name: "flag is normalized", flag: "./scripts//test.sh", want: "scripts/test.sh"},
		{name: "flag whitespace", flag: "  scripts/test.sh ", want: "scripts/test.sh"},
		{name: "absolute flag inside repository", flag: filepath.Join(repo, "ios", "ci.sh"), want: "ios/ci.sh"},
		{name: "neither", wantErr: "--script <path> or set ios.testScript"},
		{name: "missing from flag", flag: "scripts/missing.sh", config: "scripts/test.sh", wantErr: "does not exist"},
		{name: "missing from config", config: "scripts/missing.sh", wantErr: "ios.testScript in builder.json does not exist"},
		{name: "directory", flag: "scripts/folder.sh", wantErr: "not a regular file"},
		{name: "traversal", flag: "../outside.sh", wantErr: "inside the repository"},
		{name: "absolute outside", flag: filepath.Join(filepath.Dir(repo), "outside.sh"), wantErr: "inside the repository"},
		{name: "unclean config", config: "./scripts/test.sh", wantErr: "clean path"},
		{name: "git metadata", flag: ".git/hooks/pre-commit", wantErr: "Git metadata"},
		{name: "shell characters", flag: "scripts/test.sh;id", wantErr: "may contain only"},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests, struct{ name, flag, config, want, wantErr string }{
			name: "backslash", flag: `scripts\test.sh`, wantErr: "forward slashes",
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveTestScript(test.flag, test.config, repo)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("resolveTestScript() = %q, %v; want error containing %q", got, err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolveTestScript() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestResolveTestScriptRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	writeFile(t, filepath.Join(parent, "outside.sh"), "exit 0\n")
	writeFile(t, filepath.Join(repo, "scripts", "inside.sh"), "exit 0\n")
	if err := os.Symlink(filepath.Join(parent, "outside.sh"), filepath.Join(repo, "scripts", "escape.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.sh", filepath.Join(repo, "scripts", "alias.sh")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTestScript("scripts/escape.sh", "", repo); err == nil || !strings.Contains(err.Error(), "outside the repository") {
		t.Fatalf("symlink escape error = %v", err)
	}
	if got, err := resolveTestScript("scripts/alias.sh", "", repo); err != nil || got != "scripts/alias.sh" {
		t.Fatalf("symlink inside the repository = %q, %v", got, err)
	}
}

func TestTestScriptIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("init", "-q")
	writeFile(t, filepath.Join(repo, ".gitignore"), "local/\ntracked.sh\n")
	writeFile(t, filepath.Join(repo, "local", "test.sh"), "exit 0\n")
	writeFile(t, filepath.Join(repo, "scripts", "test.sh"), "exit 0\n")
	writeFile(t, filepath.Join(repo, "tracked.sh"), "exit 0\n")
	run("add", "-f", "tracked.sh")
	for script, want := range map[string]bool{"local/test.sh": true, "scripts/test.sh": false, "tracked.sh": false} {
		got, err := testScriptIgnored(context.Background(), repo, script)
		if err != nil || got != want {
			t.Errorf("testScriptIgnored(%q) = %v, %v; want %v", script, got, err, want)
		}
	}
}

func TestTerminalSafe(t *testing.T) {
	got := terminalSafe([]byte("# Report\r\n\x1b]0;title\x07\x1b[2Jok\tdone \u0085\xff\n"))
	if got != "# Report\n]0;title[2Jok\tdone \uFFFD\n" {
		t.Fatalf("terminalSafe() = %q", got)
	}
}

func TestPrintTestResult(t *testing.T) {
	var passed bytes.Buffer
	printTestResult(&passed, &build.TestResult{
		Passed: true, Conclusion: "success", Report: []byte("## 12 tests passed"),
		ReportPath: "dist/ios-test-id.md", LogPath: "dist/ios-test-id.log", WorkflowURL: "https://github.com/o/r/actions/runs/1",
		RunDeleted: true,
	})
	for _, want := range []string{"## 12 tests passed\n", "Tests passed\n", "Report: dist/ios-test-id.md\n", "Log: dist/ios-test-id.log\n", "Workflow run and its encrypted artifact deleted from the public builder\n"} {
		if !strings.Contains(passed.String(), want) {
			t.Errorf("passing output missing %q:\n%s", want, passed.String())
		}
	}
	if strings.Contains(passed.String(), "actions/runs/1") || strings.Contains(passed.String(), "Artifact:") {
		t.Errorf("passing output names a deleted run or a missing artifact:\n%s", passed.String())
	}
	var failed bytes.Buffer
	printTestResult(&failed, &build.TestResult{Conclusion: "failure", LogPath: "dist/ios-test-id.log", WorkflowURL: "https://github.com/o/r/actions/runs/2", RunKept: build.RunKeptFailed})
	for _, want := range []string{
		"wrote no $BUILDER_REPORT_DIR/report.md", "Tests failed (workflow concluded failure)\n",
		"Workflow: https://github.com/o/r/actions/runs/2 (kept because the tests did not pass; its encrypted artifact expires after one day)\n",
	} {
		if !strings.Contains(failed.String(), want) {
			t.Errorf("failing output missing %q:\n%s", want, failed.String())
		}
	}
	if strings.Contains(failed.String(), "Report:") {
		t.Errorf("failing output names a report that does not exist:\n%s", failed.String())
	}
	var kept bytes.Buffer
	printTestResult(&kept, &build.TestResult{
		Passed: true, Conclusion: "success", LogPath: "dist/windows-test-id.log", WorkflowURL: "https://github.com/o/r/actions/runs/3",
		ArtifactPath: "dist/EntrixSetup.exe", ArtifactSize: 160 << 20, RunKept: build.RunKeptOnRequest,
	})
	for _, want := range []string{"Tests passed\n", "Artifact: dist/EntrixSetup.exe (160.0 MB)\n", "Workflow: https://github.com/o/r/actions/runs/3 (kept because --keep-run was given)\n"} {
		if !strings.Contains(kept.String(), want) {
			t.Errorf("kept output missing %q:\n%s", want, kept.String())
		}
	}
}

func TestIOSTestCommandFlags(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"ios", "test"})
	if err != nil || found != iosTestCmd {
		t.Fatalf("builder ios test is not registered: %v", err)
	}
	for name, want := range map[string]string{"script": "", "timeout": (2 * time.Hour).String(), "output": "dist", "remote": "origin", "keep-run": "false"} {
		flag := iosTestCmd.Flags().Lookup(name)
		if flag == nil || flag.DefValue != want {
			t.Errorf("--%s default = %v, want %q", name, flag, want)
		}
	}
	if flag := iosTestCmd.Flags().ShorthandLookup("o"); flag == nil || flag.Name != "output" {
		t.Error("-o is not the output directory")
	}
	if err := iosTestCmd.Args(iosTestCmd, []string{"scripts/test.sh"}); err == nil {
		t.Error("a positional script argument was accepted; use --script")
	}
}
