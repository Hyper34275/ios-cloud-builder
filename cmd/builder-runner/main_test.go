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
