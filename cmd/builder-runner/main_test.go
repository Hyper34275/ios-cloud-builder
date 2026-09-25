package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/MobAI-App/ios-builder/internal/runner"
)

// runAsMainEnv makes the test binary act as builder-runner itself, so the
// tests below observe the real process's stdout and stderr exactly as the
// public job log would.
const runAsMainEnv = "IOS_BUILDER_RUNNER_TEST_AS_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runAsMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const leakyTestScript = `echo "PRIVATE-STDOUT-MARKER"
echo "PRIVATE-STDERR-MARKER" >&2
echo "::error title=PRIVATE-ANNOTATION-MARKER::workflow command"
echo "PRIVATE-SUMMARY-MARKER" >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
echo "leak=PRIVATE-OUTPUT-MARKER" >> "${GITHUB_OUTPUT:-/dev/null}"
echo "LEAK=1" >> "${GITHUB_ENV:-/dev/null}"
echo "token=${ACTIONS_RUNTIME_TOKEN:-unset} oidc=${ACTIONS_ID_TOKEN_REQUEST_TOKEN:-unset}"
printf '# Report\n\nPRIVATE-REPORT-MARKER\n' > "$BUILDER_REPORT_DIR/report.md"
exit "$WANT_EXIT"
`

func TestExecuteTestsPrintsOnlyFixedStatusLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test scripts run on the macOS runner; Windows has no reliable POSIX bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	for _, test := range []struct {
		name, exit string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name: "passing", exit: "0", wantCode: 0,
			wantStdout: "Running project tests; detailed output is private\nTests passed; encrypted report is ready\n",
		},
		{
			name: "failing", exit: "3", wantCode: 1,
			wantStdout: "Running project tests; detailed output is private\n",
			wantStderr: runner.ErrTestsFailed.Error() + "\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			source := filepath.Join(root, "source")
			writeFile(t, filepath.Join(source, ".git", "config"), "[core]\n\tbare = false\n")
			writeFile(t, filepath.Join(source, "scripts", "test.sh"), leakyTestScript)
			publicFiles := map[string]string{}
			for _, name := range []string{"GITHUB_STEP_SUMMARY", "GITHUB_OUTPUT", "GITHUB_ENV"} {
				publicFiles[name] = filepath.Join(root, "runner-files", name)
				writeFile(t, publicFiles[name], "")
			}

			cmd := exec.Command(os.Args[0], "execute-tests",
				"--source", source,
				"--ios-path", ".",
				"--script", "scripts/test.sh",
				"--log", filepath.Join(root, "private-output", "test.log"),
				"--report-dir", filepath.Join(root, "private-output", "report"),
				"--timeout", "1m",
				"--recipient", identity.Recipient().String(),
				"--output", filepath.Join(root, "encrypted"),
			)
			cmd.Env = append(os.Environ(),
				runAsMainEnv+"=1",
				"WANT_EXIT="+test.exit,
				"GITHUB_STEP_SUMMARY="+publicFiles["GITHUB_STEP_SUMMARY"],
				"GITHUB_OUTPUT="+publicFiles["GITHUB_OUTPUT"],
				"GITHUB_ENV="+publicFiles["GITHUB_ENV"],
				"ACTIONS_RUNTIME_TOKEN=runtime-secret",
				"ACTIONS_ID_TOKEN_REQUEST_TOKEN=oidc-secret",
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			code := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}

			if code != test.wantCode || stdout.String() != test.wantStdout || stderr.String() != test.wantStderr {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit %d, stdout %q, stderr %q",
					code, stdout.String(), stderr.String(), test.wantCode, test.wantStdout, test.wantStderr)
			}
			for name, path := range publicFiles {
				if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
					t.Errorf("the script wrote to %s: %q, %v", name, data, err)
				}
			}

			encrypted := filepath.Join(root, "encrypted")
			if got := names(t, encrypted); strings.Join(got, ",") != "report.md.age,test.log.age" {
				t.Fatalf("encrypted members = %v", got)
			}
			if got := names(t, filepath.Join(root, "private-output")); len(got) != 0 {
				t.Fatalf("plaintext left behind: %v", got)
			}
			log := decrypt(t, filepath.Join(encrypted, "test.log.age"), identity)
			for _, want := range []string{
				"PRIVATE-STDOUT-MARKER", "PRIVATE-STDERR-MARKER", "PRIVATE-ANNOTATION-MARKER",
				"token=unset oidc=unset", "The test script exited with status " + test.exit + ".",
			} {
				if !strings.Contains(log, want) {
					t.Errorf("decrypted log missing %q:\n%s", want, log)
				}
			}
			if report := decrypt(t, filepath.Join(encrypted, "report.md.age"), identity); report != "# Report\n\nPRIVATE-REPORT-MARKER\n" {
				t.Errorf("decrypted report = %q", report)
			}
		})
	}
}

const leakyPowerShellScript = `Write-Output "PRIVATE-STDOUT-MARKER"
[Console]::Error.WriteLine("PRIVATE-STDERR-MARKER")
Write-Output "::error title=PRIVATE-ANNOTATION-MARKER::workflow command"
foreach ($name in 'GITHUB_STEP_SUMMARY', 'GITHUB_OUTPUT', 'GITHUB_ENV', 'GITHUB_PATH') {
  $file = [Environment]::GetEnvironmentVariable($name)
  if ($file) { Add-Content -LiteralPath $file -Value "PRIVATE-$name-MARKER" }
}
Write-Output ("token=" + ($null -eq $env:ACTIONS_RUNTIME_TOKEN) + " oidc=" + ($null -eq $env:ACTIONS_ID_TOKEN_REQUEST_TOKEN))
New-Item -ItemType Directory -Force -Path dist | Out-Null
[IO.File]::WriteAllText((Join-Path $env:BUILDER_SOURCE_DIR 'dist/App.exe'), 'PRIVATE-ARTIFACT-MARKER')
Set-Content -LiteralPath (Join-Path $env:BUILDER_REPORT_DIR 'report.md') -Value 'PRIVATE-REPORT-MARKER'
exit [int]$env:WANT_EXIT
`

// TestExecuteTestsPowerShellPrintsOnlyFixedStatusLines is the Windows job's
// execute-tests call: a .ps1 script with an artifact. It runs wherever pwsh is
// installed, which includes the Windows CI runner.
func TestExecuteTestsPowerShellPrintsOnlyFixedStatusLines(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Skip("pwsh is not installed")
	}
	for _, test := range []struct {
		name, exit   string
		wantCode     int
		wantStdout   string
		wantStderr   string
		wantMembers  string
		wantExitNote string
	}{
		{
			name: "passing", exit: "0", wantCode: 0,
			wantStdout:  "Running project tests; detailed output is private\nTests passed; encrypted report and artifact are ready\n",
			wantMembers: "artifact.age,report.md.age,test.log.age",
		},
		{
			name: "failing", exit: "3", wantCode: 1,
			wantStdout:  "Running project tests; detailed output is private\n",
			wantStderr:  runner.ErrTestsFailed.Error() + "\n",
			wantMembers: "report.md.age,test.log.age",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			source := filepath.Join(root, "source")
			writeFile(t, filepath.Join(source, ".git", "config"), "[core]\n\tbare = false\n")
			writeFile(t, filepath.Join(source, "scripts", "windows-test.ps1"), leakyPowerShellScript)
			publicFiles := map[string]string{}
			for _, name := range []string{"GITHUB_STEP_SUMMARY", "GITHUB_OUTPUT", "GITHUB_ENV", "GITHUB_PATH"} {
				publicFiles[name] = filepath.Join(root, "runner-files", name)
				writeFile(t, publicFiles[name], "")
			}
			cmd := exec.Command(os.Args[0], "execute-tests",
				"--source="+source,
				"--script=scripts/windows-test.ps1",
				"--artifact=dist/App.exe",
				"--log="+filepath.Join(root, "private-output", "test.log"),
				"--report-dir="+filepath.Join(root, "private-output", "report"),
				"--timeout=2m",
				"--recipient="+identity.Recipient().String(),
				"--output="+filepath.Join(root, "encrypted"),
			)
			cmd.Env = append(os.Environ(),
				runAsMainEnv+"=1",
				"WANT_EXIT="+test.exit,
				"GITHUB_STEP_SUMMARY="+publicFiles["GITHUB_STEP_SUMMARY"],
				"GITHUB_OUTPUT="+publicFiles["GITHUB_OUTPUT"],
				"GITHUB_ENV="+publicFiles["GITHUB_ENV"],
				"GITHUB_PATH="+publicFiles["GITHUB_PATH"],
				"ACTIONS_RUNTIME_TOKEN=runtime-secret",
				"ACTIONS_ID_TOKEN_REQUEST_TOKEN=oidc-secret",
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			code := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != test.wantCode || stdout.String() != test.wantStdout || stderr.String() != test.wantStderr {
				t.Fatalf("exit %d, stdout %q, stderr %q; want exit %d, stdout %q, stderr %q",
					code, stdout.String(), stderr.String(), test.wantCode, test.wantStdout, test.wantStderr)
			}
			for name, path := range publicFiles {
				if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
					t.Errorf("the script wrote to %s: %q, %v", name, data, err)
				}
			}
			encrypted := filepath.Join(root, "encrypted")
			if got := strings.Join(names(t, encrypted), ","); got != test.wantMembers {
				t.Fatalf("encrypted members = %v, want %v", got, test.wantMembers)
			}
			if got := names(t, filepath.Join(root, "private-output")); len(got) != 0 {
				t.Fatalf("plaintext left behind: %v", got)
			}
			log := decrypt(t, filepath.Join(encrypted, "test.log.age"), identity)
			for _, want := range []string{
				"PRIVATE-STDOUT-MARKER", "PRIVATE-STDERR-MARKER", "PRIVATE-ANNOTATION-MARKER",
				"token=True oidc=True", "The test script exited with status " + test.exit + ".",
			} {
				if !strings.Contains(log, want) {
					t.Errorf("decrypted log missing %q:\n%s", want, log)
				}
			}
			if test.wantCode == 0 {
				if artifact := decrypt(t, filepath.Join(encrypted, "artifact.age"), identity); artifact != "PRIVATE-ARTIFACT-MARKER" {
					t.Errorf("decrypted artifact = %q", artifact)
				}
			}
		})
	}
}

func TestExecuteTestsRejectsBadArgumentsWithoutEchoingThem(t *testing.T) {
	cmd := exec.Command(os.Args[0], "execute-tests", "--source", "relative", "--script", "../PRIVATE-PATH.sh", "--timeout", "1m")
	cmd.Env = append(os.Environ(), runAsMainEnv+"=1")
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("exit = %v, want 1", err)
	}
	if strings.Contains(string(output), "PRIVATE-PATH") || !strings.Contains(string(output), "secure test artifact preparation failed") {
		t.Fatalf("output = %q", output)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Name())
	}
	sort.Strings(result)
	return result
}

func decrypt(t *testing.T, path string, identity age.Identity) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := age.Decrypt(file, identity)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestValidateInputsAcceptsTheWindowsJobArguments passes arguments exactly as
// the windows-test job does: one --name=value each, empty values included.
func TestValidateInputsAcceptsTheWindowsJobArguments(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	buildID := "123e4567-e89b-42d3-a456-426614174000"
	arguments := func(operation, script, artifact string) []string {
		return []string{
			"validate-inputs",
			"--build-id=" + buildID,
			"--source-owner=owner",
			"--source-repo=private",
			"--snapshot-ref=refs/ios-builder/jobs/" + buildID,
			"--ios-path=.",
			"--scheme=",
			"--configuration=Release",
			"--framework-hint=auto",
			"--artifact-recipient=" + identity.Recipient().String(),
			"--operation=" + operation,
			"--test-script=" + script,
			"--artifact-path=" + artifact,
		}
	}
	for _, test := range []struct {
		name     string
		args     []string
		wantCode int
	}{
		{"windows test with artifact", arguments("windows-test", "scripts/windows-test.ps1", "dist/EntrixSetup.exe"), 0},
		{"windows test without artifact", arguments("windows-test", "scripts/windows-test.ps1", ""), 0},
		{"windows test with a cmd script", arguments("windows-test", "scripts/windows-test.cmd", ""), 1},
		{"hostile artifact", arguments("windows-test", "scripts/windows-test.ps1", "../PRIVATE-PATH.exe"), 1},
		{"artifact on a build", arguments("build", "", "dist/EntrixSetup.exe"), 1},
		{"artifact on an iOS test", arguments("test", "scripts/test.sh", "dist/EntrixSetup.exe"), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], test.args...)
			cmd.Env = append(os.Environ(), runAsMainEnv+"=1")
			output, err := cmd.CombinedOutput()
			code := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != test.wantCode || strings.Contains(string(output), "PRIVATE-PATH") {
				t.Fatalf("exit %d, output %q; want exit %d without echoing the value", code, output, test.wantCode)
			}
		})
	}
}
