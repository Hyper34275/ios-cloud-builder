package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/build"
)

func TestWindowsTestCommandFlags(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"windows", "test"})
	if err != nil || found != windowsTestCmd {
		t.Fatalf("builder windows test is not registered: %v", err)
	}
	for name, want := range map[string]string{
		"script": "", "artifact": "", "timeout": (150 * time.Minute).String(), "output": "dist", "remote": "origin", "keep-run": "false",
	} {
		flag := windowsTestCmd.Flags().Lookup(name)
		if flag == nil || flag.DefValue != want {
			t.Errorf("--%s default = %v, want %q", name, flag, want)
		}
	}
	if flag := windowsTestCmd.Flags().ShorthandLookup("o"); flag == nil || flag.Name != "output" {
		t.Error("-o is not the output directory")
	}
	if err := windowsTestCmd.Args(windowsTestCmd, []string{"scripts/windows-test.ps1"}); err == nil {
		t.Error("a positional script argument was accepted; use --script")
	}
	if err := windowsTestCmd.ParseFlags([]string{"--script", "s.ps1", "--artifact", "dist/x.exe", "--timeout", "2h30m", "-o", "out", "--keep-run"}); err != nil {
		t.Fatalf("flags did not parse: %v", err)
	}
	defer func() {
		for _, name := range []string{"script", "artifact", "timeout", "output", "keep-run"} {
			flag := windowsTestCmd.Flags().Lookup(name)
			_ = flag.Value.Set(flag.DefValue)
			flag.Changed = false
		}
	}()
	timeout, _ := windowsTestCmd.Flags().GetDuration("timeout")
	keepRun, _ := windowsTestCmd.Flags().GetBool("keep-run")
	if timeout != 150*time.Minute || !keepRun || !windowsTestCmd.Flags().Changed("artifact") {
		t.Errorf("parsed timeout %s, keep-run %v", timeout, keepRun)
	}
}

func TestResolveWindowsTestScript(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "scripts", "windows-test.ps1"), "exit 0\n")
	writeFile(t, filepath.Join(repo, "scripts", "windows.sh"), "exit 0\n")
	writeFile(t, filepath.Join(repo, "scripts", "windows.cmd"), "exit /b 0\n")
	for _, test := range []struct{ name, flag, config, want, wantErr string }{
		{name: "config", config: "scripts/windows-test.ps1", want: "scripts/windows-test.ps1"},
		{name: "flag overrides config", flag: "scripts/windows.sh", config: "scripts/windows-test.ps1", want: "scripts/windows.sh"},
		{name: "absolute flag", flag: filepath.Join(repo, "scripts", "windows-test.ps1"), want: "scripts/windows-test.ps1"},
		{name: "neither", wantErr: "set windows.testScript in builder.json"},
		{name: "cmd script", flag: "scripts/windows.cmd", wantErr: "must end in .ps1"},
		{name: "missing", config: "scripts/missing.ps1", wantErr: "windows.testScript in builder.json does not exist"},
		{name: "traversal", flag: "../windows-test.ps1", wantErr: "inside the repository"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveScriptPath(test.flag, test.config, windowsScriptSource, repo)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("resolveScriptPath() = %q, %v; want error containing %q", got, err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolveScriptPath() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestResolveArtifactPath(t *testing.T) {
	repo := t.TempDir()
	for _, test := range []struct {
		name, flag    string
		flagSet       bool
		config, want  string
		wantErrSubstr string
	}{
		{name: "config", config: "dist/EntrixSetup.exe", want: "dist/EntrixSetup.exe"},
		{name: "flag overrides config", flag: "out/Setup.msi", flagSet: true, config: "dist/EntrixSetup.exe", want: "out/Setup.msi"},
		{name: "flag is normalized", flag: "./dist//EntrixSetup.exe", flagSet: true, want: "dist/EntrixSetup.exe"},
		{name: "absolute flag", flag: filepath.Join(repo, "dist", "EntrixSetup.exe"), flagSet: true, want: "dist/EntrixSetup.exe"},
		{name: "empty flag disables the configured artifact", flag: "", flagSet: true, config: "dist/EntrixSetup.exe", want: ""},
		{name: "none", want: ""},
		{name: "need not exist yet", config: "dist/not-built-yet.exe", want: "dist/not-built-yet.exe"},
		{name: "traversal", flag: "../EntrixSetup.exe", flagSet: true, wantErrSubstr: "inside the repository"},
		{name: "space", config: "dist/Entrix Setup.exe", wantErrSubstr: "windows.artifact in builder.json may contain only"},
		{name: "git metadata", flag: ".git/config", flagSet: true, wantErrSubstr: "Git metadata"},
		{name: "alternate data stream", flag: "dist/EntrixSetup.exe:x", flagSet: true, wantErrSubstr: "may contain only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveArtifactPath(test.flag, test.flagSet, test.config, repo)
			if test.wantErrSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErrSubstr) {
					t.Fatalf("resolveArtifactPath() = %q, %v; want error containing %q", got, err, test.wantErrSubstr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("resolveArtifactPath() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestWarnIfArtifactWouldBeSnapshotted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	if output, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	writeFile(t, filepath.Join(repo, ".gitignore"), "dist/\n")
	t.Chdir(repo)
	for _, test := range []struct {
		output, artifact string
		warn             bool
	}{
		{"dist", "dist/EntrixSetup.exe", false},           // ignored
		{"out", "dist/EntrixSetup.exe", true},             // out/EntrixSetup.exe is not ignored
		{filepath.Join(t.TempDir(), "x"), "a.exe", false}, // outside the repository
		{"out", "", false},                                // no artifact
	} {
		var warning bytes.Buffer
		warnIfArtifactWouldBeSnapshotted(context.Background(), &warning, repo, test.output, test.artifact)
		if got := strings.Contains(warning.String(), "not excluded by .gitignore"); got != test.warn {
			t.Errorf("output %q artifact %q: warning %q, want warning %v", test.output, test.artifact, warning.String(), test.warn)
		}
	}
}

func TestPrintWindowsTestResult(t *testing.T) {
	var failed bytes.Buffer
	printTestResult(&failed, &build.TestResult{
		Platform: build.PlatformWindows, Conclusion: "failure", LogPath: "dist/windows-test-id.log",
		WorkflowURL: "https://github.com/o/r/actions/runs/9", RunKept: build.RunKeptFailed,
	})
	if strings.Contains(failed.String(), "Artifact:") || !strings.Contains(failed.String(), "kept because the tests did not pass") {
		t.Fatalf("failing Windows output:\n%s", failed.String())
	}
}
