package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/MobAI-App/ios-builder/internal/config"
)

// testScriptPaths are shared by the runner and CLI validation tests: both sides
// must agree, or the CLI would dispatch runs the workflow rejects.
var (
	goodTestScriptPaths = []string{
		"test.sh",
		"scripts/test.sh",
		"ios/Scripts/run-tests.sh",
		".ci/test.sh",
		".github/scripts/ios_test+unit.sh",
		"a/b/c/d/e/f/g/h.sh",
		strings.Repeat("a", MaxTestScriptPathLength),
	}
	hostileTestScriptPaths = []string{
		"",
		"/etc/passwd",
		"/scripts/test.sh",
		"../test.sh",
		"scripts/../../test.sh",
		"scripts/../test.sh",
		"..",
		".",
		"./test.sh",
		"scripts/./test.sh",
		"scripts//test.sh",
		"scripts/",
		`scripts\test.sh`,
		`C:\test.sh`,
		"C:/test.sh",
		"~/test.sh",
		".git/hooks/pre-commit",
		".GIT/config",
		"sub/.Git/config",
		"sub/.git/config",
		"-x.sh",
		"scripts/--rcfile",
		"test.sh\n",
		"test\x00.sh",
		"test\r.sh",
		"test\t.sh",
		"test\x1b[2J.sh",
		"test\x7f.sh",
		"my tests.sh",
		"test.sh;id",
		"$(id).sh",
		"`id`.sh",
		"test.sh|id",
		"test.sh&&id",
		"test*.sh",
		"test?.sh",
		"'test'.sh",
		`"test".sh`,
		"tëst.sh",
		"test.sh\u202e",
		strings.Repeat("a", MaxTestScriptPathLength+1),
	}
)

func TestValidateTestScriptPath(t *testing.T) {
	for _, value := range goodTestScriptPaths {
		if err := ValidateTestScriptPath(value); err != nil {
			t.Errorf("ValidateTestScriptPath(%q) = %v, want nil", value, err)
		}
	}
	for _, value := range hostileTestScriptPaths {
		err := ValidateTestScriptPath(value)
		if err == nil {
			t.Errorf("ValidateTestScriptPath(%q) accepted a hostile path", value)
			continue
		}
		if value != "" && strings.Contains(err.Error(), value) {
			t.Errorf("ValidateTestScriptPath(%q) echoed the value in %q", value, err)
		}
	}
}

func TestTestScriptValidationMatchesConfig(t *testing.T) {
	for _, value := range append(append([]string{}, goodTestScriptPaths...), hostileTestScriptPaths...) {
		runnerErr := ValidateTestScriptPath(value)
		configErr := config.ValidateTestScriptPath(value)
		if (runnerErr == nil) != (configErr == nil) {
			t.Errorf("%q: runner error %v, config error %v; the CLI and workflow must agree", value, runnerErr, configErr)
		}
	}
}

func TestInputsValidateTestOperation(t *testing.T) {
	in := validInputs(t)
	in.Operation = "test"
	in.TestScript = "scripts/test.sh"
	if err := in.Validate(); err != nil {
		t.Fatalf("valid test inputs rejected: %v", err)
	}
	for _, script := range hostileTestScriptPaths {
		hostile := in
		hostile.TestScript = script
		if err := hostile.Validate(); err == nil {
			t.Errorf("test_script %q accepted", script)
		}
	}
	for _, operation := range []string{"build", "testflight"} {
		other := in
		other.Operation = operation
		if err := other.Validate(); err == nil {
			t.Errorf("operation %s accepted a test_script", operation)
		}
		other.TestScript = ""
		if err := other.Validate(); err != nil {
			t.Errorf("operation %s without test_script rejected: %v", operation, err)
		}
	}
}

func TestTestEnvironmentScrubsPublishingAndCredentials(t *testing.T) {
	base := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/Users/runner",
		"DEVELOPER_DIR=/Applications/Xcode_26.3.app/Contents/Developer",
		"CI=true",
		"GITHUB_ACTIONS=true",
		"GITHUB_WORKSPACE=/work",
		"RUNNER_TEMP=/runner/temp",
		"GITHUB_STEP_SUMMARY=/runner/summary",
		"GITHUB_OUTPUT=/runner/output",
		"GITHUB_ENV=/runner/env",
		"GITHUB_PATH=/runner/path",
		"GITHUB_STATE=/runner/state",
		"GITHUB_TOKEN=ghs_secret",
		"github_token=ghs_lowercase_secret",
		"GH_TOKEN=gho_secret",
		"SOURCE_TOKEN=ghs_source_secret",
		"ACTIONS_RUNTIME_TOKEN=runtime-secret",
		"ACTIONS_RUNTIME_URL=https://runtime.invalid/",
		"ACTIONS_RESULTS_URL=https://results.invalid/",
		"ACTIONS_CACHE_URL=https://cache.invalid/",
		"ACTIONS_CACHE_SERVICE_V2=true",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=oidc-secret",
		"ACTIONS_ID_TOKEN_REQUEST_URL=https://oidc.invalid/",
		"ACTIONS_ORCHESTRATION_ID=orchestration",
		"INPUT_TOKEN=input-secret",
		"APP_PRIVATE_KEY=pem-secret",
		"APPLE_DISTRIBUTION_P12_PASSWORD=p12-secret",
		"ASC_CLIENT_SECRET=asc-secret",
		"APPLE_SIGNING_AGE_IDENTITY=AGE-SECRET-KEY-1",
		"GIT_CREDENTIALS=https://user:pass@example.invalid",
		"BUILDER_SOURCE_DIR=/spoofed",
		"BUILDER_REPORT_DIR=/spoofed",
	}
	env := TestEnvironment(base, "/work/source", "ios", "/runner/temp/private-output/report")
	values := map[string]string{}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if _, duplicate := values[name]; duplicate {
			t.Errorf("duplicate variable %s", name)
		}
		values[name] = value
	}
	for _, removed := range []string{
		"GITHUB_STEP_SUMMARY", "GITHUB_OUTPUT", "GITHUB_ENV", "GITHUB_PATH", "GITHUB_STATE",
		"GITHUB_TOKEN", "github_token", "GH_TOKEN", "SOURCE_TOKEN",
		"ACTIONS_RUNTIME_TOKEN", "ACTIONS_RUNTIME_URL", "ACTIONS_RESULTS_URL", "ACTIONS_CACHE_URL",
		"ACTIONS_CACHE_SERVICE_V2", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_URL",
		"ACTIONS_ORCHESTRATION_ID", "INPUT_TOKEN", "APP_PRIVATE_KEY", "APPLE_DISTRIBUTION_P12_PASSWORD",
		"ASC_CLIENT_SECRET", "APPLE_SIGNING_AGE_IDENTITY", "GIT_CREDENTIALS",
	} {
		if _, present := values[removed]; present {
			t.Errorf("test environment kept %s", removed)
		}
	}
	joined := strings.Join(env, "\n")
	for _, secret := range []string{"secret", "/spoofed", "AGE-SECRET-KEY", "user:pass"} {
		if strings.Contains(joined, secret) {
			t.Errorf("test environment leaked %q", secret)
		}
	}
	for name, want := range map[string]string{
		"PATH":               "/usr/bin:/bin",
		"HOME":               "/Users/runner",
		"DEVELOPER_DIR":      "/Applications/Xcode_26.3.app/Contents/Developer",
		"CI":                 "true",
		"GITHUB_ACTIONS":     "true",
		"GITHUB_WORKSPACE":   "/work",
		"RUNNER_TEMP":        "/runner/temp",
		"BUILDER_SOURCE_DIR": "/work/source",
		"BUILDER_IOS_PATH":   "ios",
		"BUILDER_REPORT_DIR": "/runner/temp/private-output/report",
	} {
		if values[name] != want {
			t.Errorf("%s = %q, want %q", name, values[name], want)
		}
	}
}

type testCheckout struct {
	root, source, privateDir, encrypted string
	identity                            *age.X25519Identity
}

func requireBash(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test scripts run on the macOS runner; Windows has no reliable POSIX bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
}

func newTestCheckout(t *testing.T, script string) *testCheckout {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved // macOS temp directories live behind /var -> /private/var
	}
	checkout := &testCheckout{
		root:       root,
		source:     filepath.Join(root, "source"),
		privateDir: filepath.Join(root, "private-output"),
		encrypted:  filepath.Join(root, "encrypted"),
		identity:   identity,
	}
	writeTestFile(t, filepath.Join(checkout.source, ".git", "config"), "[core]\n\tbare = false\n")
	if script != "" {
		writeTestFile(t, filepath.Join(checkout.source, "scripts", "test.sh"), script)
	}
	return checkout
}

func (c *testCheckout) options(timeout time.Duration) *TestOptions {
	return &TestOptions{
		SourceRoot: c.source,
		IOSPath:    "ios",
		Script:     "scripts/test.sh",
		LogPath:    filepath.Join(c.privateDir, "test.log"),
		ReportDir:  filepath.Join(c.privateDir, "report"),
		Timeout:    timeout,
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRunTestsRoutesOutputToPrivateLogAndPropagatesExitCode(t *testing.T) {
	requireBash(t)
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(t.TempDir(), "summary"))
	t.Setenv("GITHUB_OUTPUT", filepath.Join(t.TempDir(), "output"))
	t.Setenv("ACTIONS_RUNTIME_TOKEN", "runtime-secret")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "oidc-secret")
	for _, exitCode := range []int{0, 1, 3, 125} {
		t.Run(fmt.Sprintf("exit %d", exitCode), func(t *testing.T) {
			checkout := newTestCheckout(t, fmt.Sprintf(`echo "stdout-marker"
echo "stderr-marker" >&2
echo "cwd=$(pwd -P)"
echo "source=$BUILDER_SOURCE_DIR ios=$BUILDER_IOS_PATH"
test -d "$BUILDER_REPORT_DIR" && test -z "$(ls -A "$BUILDER_REPORT_DIR")" && echo "report-dir-empty"
echo "summary=${GITHUB_STEP_SUMMARY-unset} output=${GITHUB_OUTPUT-unset}"
echo "runtime=${ACTIONS_RUNTIME_TOKEN-unset} oidc=${ACTIONS_ID_TOKEN_REQUEST_TOKEN-unset}"
exit %d
`, exitCode))
			options := checkout.options(time.Minute)
			outcome, err := RunTests(context.Background(), options)
			if err != nil {
				t.Fatalf("RunTests() error = %v", err)
			}
			if outcome.ExitCode != exitCode || outcome.TimedOut || outcome.Passed() != (exitCode == 0) {
				t.Fatalf("outcome = %+v, want exit %d", outcome, exitCode)
			}
			log := readTestFile(t, options.LogPath)
			for _, want := range []string{
				"stdout-marker", "stderr-marker",
				"cwd=" + checkout.source,
				"source=" + checkout.source + " ios=ios",
				"report-dir-empty",
				"summary=unset output=unset",
				"runtime=unset oidc=unset",
				fmt.Sprintf("The test script exited with status %d.", exitCode),
			} {
				if !strings.Contains(log, want) {
					t.Errorf("private log missing %q:\n%s", want, log)
				}
			}
		})
	}
}

func TestRunTestsTimeoutStopsTheWholeProcessGroup(t *testing.T) {
	requireBash(t)
	checkout := newTestCheckout(t, "")
	marker := filepath.Join(checkout.root, "late-marker")
	writeTestFile(t, filepath.Join(checkout.source, "scripts", "test.sh"), fmt.Sprintf(`echo started
(sleep 2; echo late > '%s') &
sleep 60
`, marker))
	started := time.Now()
	outcome, err := RunTests(context.Background(), checkout.options(time.Second))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("RunTests() error = %v", err)
	}
	if !outcome.TimedOut || outcome.Passed() {
		t.Fatalf("outcome = %+v, want a timeout", outcome)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("RunTests() took %s after a 1s timeout", elapsed)
	}
	log := readTestFile(t, checkout.options(time.Second).LogPath)
	if !strings.Contains(log, "started") || !strings.Contains(log, "timed out after 1s") {
		t.Fatalf("private log does not record the timeout:\n%s", log)
	}
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a background process outlived the timed-out script")
	}
}

func TestRunTestsStopsBackgroundProcessesAfterExit(t *testing.T) {
	requireBash(t)
	checkout := newTestCheckout(t, "")
	marker := filepath.Join(checkout.root, "late-marker")
	writeTestFile(t, filepath.Join(checkout.source, "scripts", "test.sh"), fmt.Sprintf(`(sleep 1; echo late > '%s') &
exit 0
`, marker))
	outcome, err := RunTests(context.Background(), checkout.options(time.Minute))
	if err != nil || !outcome.Passed() {
		t.Fatalf("RunTests() = %+v, %v", outcome, err)
	}
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a background process kept running after the script exited")
	}
}

func TestRunTestsRecordsUnrunnableScriptPrivately(t *testing.T) {
	checkout := newTestCheckout(t, "")
	options := checkout.options(time.Minute)
	outcome, err := RunTests(context.Background(), options)
	if err == nil || outcome.Passed() {
		t.Fatalf("RunTests() = %+v, %v; want an error for a missing script", outcome, err)
	}
	if log := readTestFile(t, options.LogPath); !strings.Contains(log, "does not exist in the snapshot") {
		t.Fatalf("private log does not explain the failure:\n%s", log)
	}
}

func TestResolveTestScriptRejectsHostileTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	checkout := newTestCheckout(t, "exit 0\n")
	outside := filepath.Join(checkout.root, "outside.sh")
	writeTestFile(t, outside, "exit 0\n")
	scripts := filepath.Join(checkout.source, "scripts")
	for name, target := range map[string]string{
		"escape.sh":   outside,
		"relative.sh": "../../outside.sh",
		"git.sh":      "../.git/config",
		"dir.sh":      ".",
		"dangling.sh": "missing.sh",
		"inside.sh":   "test.sh",
	} {
		if err := os.Symlink(target, filepath.Join(scripts, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(scripts, "folder.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(checkout.root, filepath.Join(checkout.source, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{
		"scripts/escape.sh", "scripts/relative.sh", "scripts/git.sh", "scripts/dir.sh",
		"scripts/dangling.sh", "scripts/folder.sh", "scripts/missing.sh", "linked/outside.sh",
		"../outside.sh",
	} {
		if _, err := ResolveTestScript(checkout.source, script); err == nil {
			t.Errorf("ResolveTestScript(%q) accepted a target outside the checkout's regular files", script)
		}
	}
	for _, script := range []string{"scripts/test.sh", "scripts/inside.sh"} {
		resolved, err := ResolveTestScript(checkout.source, script)
		if err != nil || resolved != filepath.Join(scripts, "test.sh") {
			t.Errorf("ResolveTestScript(%q) = %q, %v", script, resolved, err)
		}
	}
}

func TestExecuteTestsSecureEncryptsOnlyLogAndReport(t *testing.T) {
	requireBash(t)
	for _, test := range []struct {
		name       string
		script     string
		wantErr    error
		wantReport string
	}{
		{"passing with report", "echo private-pass-output\nprintf '# Tests\\n\\nAll passed\\n' > \"$BUILDER_REPORT_DIR/report.md\"\nexit 0\n", nil, "# Tests\n\nAll passed\n"},
		{"failing with report", "echo private-fail-output\nprintf 'Failed: 2\\n' > \"$BUILDER_REPORT_DIR/report.md\"\nexit 4\n", ErrTestsFailed, "Failed: 2\n"},
		{"passing without report", "echo private-pass-output\n", nil, ""},
		{"symlinked report ignored", "ln -s \"$BUILDER_SOURCE_DIR/scripts/test.sh\" \"$BUILDER_REPORT_DIR/report.md\"\n", nil, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkout := newTestCheckout(t, test.script)
			options := checkout.options(time.Minute)
			err := ExecuteTestsSecure(context.Background(), options, checkout.identity.Recipient().String(), checkout.encrypted)
			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) {
				t.Fatalf("ExecuteTestsSecure() error = %v, want %v", err, test.wantErr)
			}
			want := []string{"test.log.age"}
			if test.wantReport != "" {
				want = []string{"report.md.age", "test.log.age"}
			}
			if got := dirNames(t, checkout.encrypted); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("encrypted members = %v, want %v", got, want)
			}
			if leftovers := dirNames(t, checkout.privateDir); len(leftovers) != 0 {
				t.Fatalf("plaintext left in private output: %v", leftovers)
			}
			log := decryptFile(t, filepath.Join(checkout.encrypted, "test.log.age"), checkout.identity)
			if !strings.Contains(log, "The test script exited with status") {
				t.Fatalf("decrypted log = %q", log)
			}
			if test.name == "symlinked report ignored" && !strings.Contains(log, "report.md is not a regular file") {
				t.Fatalf("decrypted log does not explain the ignored report: %q", log)
			}
			if test.wantReport != "" {
				if got := decryptFile(t, filepath.Join(checkout.encrypted, "report.md.age"), checkout.identity); got != test.wantReport {
					t.Fatalf("decrypted report = %q, want %q", got, test.wantReport)
				}
			}
		})
	}
}

func TestExecuteTestsSecureRejectsUnexpectedPaths(t *testing.T) {
	checkout := newTestCheckout(t, "exit 0\n")
	recipient := checkout.identity.Recipient().String()
	valid := checkout.options(time.Minute)
	for name, mutate := range map[string]func(*TestOptions){
		"log name":         func(o *TestOptions) { o.LogPath = filepath.Join(checkout.privateDir, "build.log") },
		"log directory":    func(o *TestOptions) { o.LogPath = filepath.Join(checkout.root, "public", "test.log") },
		"report directory": func(o *TestOptions) { o.ReportDir = filepath.Join(checkout.root, "report") },
		"log in source": func(o *TestOptions) {
			o.LogPath = filepath.Join(checkout.source, "private-output", "test.log")
			o.ReportDir = filepath.Join(checkout.source, "private-output", "report")
		},
		"relative source":  func(o *TestOptions) { o.SourceRoot = "source" },
		"hostile script":   func(o *TestOptions) { o.Script = "../outside.sh" },
		"hostile ios path": func(o *TestOptions) { o.IOSPath = "../ios" },
		"no timeout":       func(o *TestOptions) { o.Timeout = 0 },
		"huge timeout":     func(o *TestOptions) { o.Timeout = MaxTestTimeout + time.Minute },
	} {
		t.Run(name, func(t *testing.T) {
			options := *valid
			mutate(&options)
			if err := ExecuteTestsSecure(context.Background(), &options, recipient, checkout.encrypted); err == nil || errors.Is(err, ErrTestsFailed) {
				t.Fatalf("ExecuteTestsSecure() error = %v, want a validation error", err)
			}
		})
	}
	if err := ExecuteTestsSecure(context.Background(), valid, recipient, filepath.Join(checkout.root, "public")); err == nil {
		t.Fatal("arbitrary encrypted output directory accepted")
	}
}

func TestEncryptTestArtifactsRejectsPlantedOutput(t *testing.T) {
	checkout := newTestCheckout(t, "")
	logPath := filepath.Join(checkout.privateDir, "test.log")
	reportPath := filepath.Join(checkout.privateDir, "report.md")
	writeTestFile(t, logPath, "private log")
	writeTestFile(t, reportPath, "private report")
	writeTestFile(t, filepath.Join(checkout.encrypted, "source.zip"), "planted")
	if err := EncryptTestArtifacts(checkout.identity.Recipient().String(), logPath, reportPath, checkout.encrypted); err == nil {
		t.Fatal("planted output member accepted")
	}
	for _, plain := range []string{logPath, reportPath} {
		if _, err := os.Stat(plain); !os.IsNotExist(err) {
			t.Fatalf("plaintext %s retained after failure", plain)
		}
	}

	// Stale allowlisted names are replaced, never uploaded as they were.
	writeTestFile(t, logPath, "private log")
	if err := os.Remove(filepath.Join(checkout.encrypted, "source.zip")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(checkout.encrypted, "report.md.age"), "plaintext preplant")
	if err := EncryptTestArtifacts(checkout.identity.Recipient().String(), logPath, reportPath, checkout.encrypted); err != nil {
		t.Fatal(err)
	}
	if got := dirNames(t, checkout.encrypted); strings.Join(got, ",") != "test.log.age" {
		t.Fatalf("encrypted members = %v", got)
	}
}

func TestStageReportCapsSize(t *testing.T) {
	root := t.TempDir()
	reportDir := filepath.Join(root, "report")
	writeTestFile(t, filepath.Join(reportDir, "report.md"), strings.Repeat("x", 100))
	destination := filepath.Join(root, "report.md")
	note, err := stageReport(reportDir, destination, 10)
	if err != nil || !strings.Contains(note, "truncated") {
		t.Fatalf("stageReport() = %q, %v", note, err)
	}
	staged := readTestFile(t, destination)
	if !strings.HasPrefix(staged, strings.Repeat("x", 10)+"\n") || strings.Contains(staged, strings.Repeat("x", 11)) || !strings.Contains(staged, "truncated") {
		t.Fatalf("staged report = %q", staged)
	}

	if note, err := stageReport(filepath.Join(root, "missing"), destination, 10); err != nil || note != "" {
		t.Fatalf("stageReport(missing) = %q, %v", note, err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("stale staged report kept")
	}
}

func TestBoundTestLogKeepsHeadAndTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	contents := "HEAD" + strings.Repeat("m", 5000) + "TAIL"
	writeTestFile(t, path, contents)
	if err := boundTestLog(path, 1000, 100); err != nil {
		t.Fatal(err)
	}
	bounded := readTestFile(t, path)
	if len(bounded) > 1000 || !strings.HasPrefix(bounded, "HEAD") || !strings.HasSuffix(bounded, "TAIL") || !strings.Contains(bounded, "omitted") {
		t.Fatalf("bounded log (%d bytes) = %q", len(bounded), bounded)
	}
	short := filepath.Join(t.TempDir(), "short.log")
	writeTestFile(t, short, "short")
	if err := boundTestLog(short, 1000, 100); err != nil || readTestFile(t, short) != "short" {
		t.Fatal("a log within its limit was changed")
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}
