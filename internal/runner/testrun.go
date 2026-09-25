package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrTestsFailed reports that the project's test script did not pass: it
// exited non-zero, timed out, or could not be started. The reason is recorded
// only in the encrypted log.
var ErrTestsFailed = errors.New("private iOS tests failed; download the encrypted report")

const (
	testLogName             = "test.log"
	testReportDirName       = "report"
	testReportName          = "report.md"
	encryptedTestLogName    = "test.log.age"
	encryptedTestReportName = "report.md.age"

	// MaxTestReportBytes caps the report.md copied out of the report directory.
	MaxTestReportBytes = 1 << 20
	// MaxTestTimeout is GitHub's limit for one hosted-runner job.
	MaxTestTimeout = 6 * time.Hour

	// The CLI refuses a log whose ciphertext exceeds 64 MiB. A longer log keeps
	// its head and tail, so a verbose test run never loses its whole result.
	maxTestLogBytes  = 60 << 20
	testLogHeadBytes = 8 << 20
	testLogNoteSpace = 256

	// testTerminateGrace is how long a timed-out script has to exit after its
	// process group receives SIGTERM before it is killed.
	testTerminateGrace = 30 * time.Second
)

// TestOptions are the structured inputs of one test-script run.
type TestOptions struct {
	SourceRoot string        // absolute private checkout; the script's working directory
	IOSPath    string        // ios_path input, relative to SourceRoot
	Script     string        // test_script input, relative to SourceRoot
	LogPath    string        // <private-output>/test.log
	ReportDir  string        // <private-output>/report
	Timeout    time.Duration // limit for the whole script
}

// TestOutcome is how the script itself ended.
type TestOutcome struct {
	ExitCode int  // the script's exit status; -1 when a signal stopped it
	TimedOut bool // the script outlived TestOptions.Timeout and was stopped
}

// Passed reports whether the script exited 0 on its own.
func (outcome TestOutcome) Passed() bool { return outcome.ExitCode == 0 && !outcome.TimedOut }

func (options *TestOptions) validate() error {
	if options == nil {
		return fmt.Errorf("missing test options")
	}
	if err := validateRelativePath(options.IOSPath); err != nil {
		return fmt.Errorf("invalid iOS path")
	}
	if err := ValidateTestScriptPath(options.Script); err != nil {
		return fmt.Errorf("invalid test script path")
	}
	if options.Timeout <= 0 || options.Timeout > MaxTestTimeout {
		return fmt.Errorf("invalid test timeout")
	}
	for _, path := range []string{options.SourceRoot, options.LogPath, options.ReportDir} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("runner paths must be absolute")
		}
	}
	privateDir := filepath.Dir(options.LogPath)
	if filepath.Base(options.LogPath) != testLogName || filepath.Base(privateDir) != "private-output" ||
		options.ReportDir != filepath.Join(privateDir, testReportDirName) ||
		pathWithin(options.SourceRoot, privateDir) {
		return fmt.Errorf("invalid private output paths")
	}
	return nil
}

// ExecuteTestsSecure runs the project's test script, then encrypts its private
// log and optional report.md to recipient in encryptedDir, which afterwards
// holds only test.log.age and report.md.age. Plaintext is removed either way.
// It returns ErrTestsFailed when the tests did not pass; any other error means
// no trustworthy ciphertext was produced.
func ExecuteTestsSecure(ctx context.Context, options *TestOptions, recipient, encryptedDir string) error {
	if err := options.validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(encryptedDir) || filepath.Base(encryptedDir) != "encrypted" ||
		filepath.Dir(encryptedDir) != filepath.Dir(filepath.Dir(options.LogPath)) {
		return fmt.Errorf("invalid encrypted output directory")
	}
	outcome, runErr := RunTests(ctx, options)
	if _, err := os.Lstat(options.LogPath); os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(options.LogPath), 0700)
		message := []byte("The test script could not start. No public diagnostic details were emitted.\n")
		_ = os.WriteFile(options.LogPath, message, 0600)
	}
	stagedReport := filepath.Join(filepath.Dir(options.LogPath), testReportName)
	note, err := stageReport(options.ReportDir, stagedReport, MaxTestReportBytes)
	if err != nil {
		note = "report.md could not be read and was ignored."
	}
	_ = os.RemoveAll(options.ReportDir)
	if note != "" {
		appendPrivateLog(options.LogPath, "\n"+note+"\n")
	}
	// A log that cannot be shortened is still encrypted; the CLI then reports
	// that it is too large instead of the run losing its diagnostics here.
	_ = boundTestLog(options.LogPath, maxTestLogBytes, testLogHeadBytes)
	if err := EncryptTestArtifacts(recipient, options.LogPath, stagedReport, encryptedDir); err != nil {
		return fmt.Errorf("encrypt private test artifacts")
	}
	if runErr != nil || !outcome.Passed() {
		return ErrTestsFailed
	}
	return nil
}

// RunTests runs `bash -- <script>` with the private checkout as its working
// directory. Its stdout and stderr go only to the private log, it receives a
// fresh empty report directory and the scrubbed TestEnvironment, and its whole
// process group is stopped once it exits or times out. An error means the
// script could not be run at all; the reason is also written to the log.
func RunTests(ctx context.Context, options *TestOptions) (TestOutcome, error) {
	if err := options.validate(); err != nil {
		return TestOutcome{ExitCode: -1}, err
	}
	if err := os.MkdirAll(filepath.Dir(options.LogPath), 0700); err != nil {
		return TestOutcome{ExitCode: -1}, fmt.Errorf("prepare private output")
	}
	logFile, err := os.OpenFile(options.LogPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return TestOutcome{ExitCode: -1}, fmt.Errorf("prepare private test log")
	}
	defer logFile.Close()
	outcome, err := runTestScript(ctx, options, logFile)
	if err != nil {
		fmt.Fprintf(logFile, "\nThe test script could not run: %v\n", err)
	}
	_ = logFile.Sync()
	return outcome, err
}

func runTestScript(ctx context.Context, options *TestOptions, privateLog *os.File) (TestOutcome, error) {
	outcome := TestOutcome{ExitCode: -1}
	sourceRoot, err := filepath.EvalSymlinks(options.SourceRoot)
	if err != nil {
		return outcome, fmt.Errorf("resolve source checkout: %w", err)
	}
	if err := VerifyCheckoutNoCredentials(sourceRoot); err != nil {
		return outcome, err
	}
	if _, err := ResolveTestScript(sourceRoot, options.Script); err != nil {
		return outcome, err
	}
	if err := resetPrivateDir(options.ReportDir); err != nil {
		return outcome, err
	}

	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	// The script is named relative to the working directory, exactly as a
	// developer runs it from the repository root; "--" ends bash's options.
	cmd := exec.CommandContext(ctx, "bash", "--", filepath.FromSlash(options.Script))
	cmd.Dir = sourceRoot
	cmd.Env = TestEnvironment(os.Environ(), sourceRoot, options.IOSPath, options.ReportDir)
	// Both streams are the log file itself rather than a pipe, so no byte of
	// output passes through this process and a lingering child that keeps the
	// descriptor open cannot delay completion.
	cmd.Stdout = privateLog
	cmd.Stderr = privateLog
	cmd.WaitDelay = testTerminateGrace
	runInOwnProcessGroup(cmd)
	fmt.Fprintf(privateLog, "$ bash -- %s\n(working directory %s, timeout %s)\n\n", options.Script, sourceRoot, options.Timeout)

	runErr := cmd.Run()
	killProcessGroup(cmd)
	if cmd.ProcessState == nil {
		return outcome, fmt.Errorf("start test script: %w", runErr)
	}
	outcome.ExitCode = cmd.ProcessState.ExitCode()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		outcome.TimedOut = true
		fmt.Fprintf(privateLog, "\nThe test script timed out after %s and was stopped.\n", options.Timeout)
	case outcome.ExitCode < 0:
		fmt.Fprintf(privateLog, "\nThe test script was stopped by a signal.\n")
	default:
		fmt.Fprintf(privateLog, "\nThe test script exited with status %d.\n", outcome.ExitCode)
	}
	return outcome, nil
}

// ResolveTestScript proves that script, once every symlink is resolved, names
// a regular file inside sourceRoot and outside its Git metadata, and returns
// the resolved path.
func ResolveTestScript(sourceRoot, script string) (string, error) {
	if err := ValidateTestScriptPath(script); err != nil {
		return "", fmt.Errorf("invalid test script path")
	}
	root, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve source checkout")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(script)))
	if err != nil {
		return "", fmt.Errorf("test script does not exist in the snapshot")
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || !pathWithin(root, resolved) {
		return "", fmt.Errorf("test script resolves outside the snapshot")
	}
	if first, _, _ := strings.Cut(filepath.ToSlash(relative), "/"); strings.EqualFold(first, ".git") {
		return "", fmt.Errorf("test script resolves into Git metadata")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("test script is not a regular file")
	}
	return resolved, nil
}

// testEnvironmentDeniedNames are the files through which a step publishes to
// the run (job summary, outputs, state) or changes later steps (env, PATH),
// and the workflow token.
var testEnvironmentDeniedNames = map[string]bool{
	"GITHUB_STEP_SUMMARY": true,
	"GITHUB_OUTPUT":       true,
	"GITHUB_ENV":          true,
	"GITHUB_PATH":         true,
	"GITHUB_STATE":        true,
	"GITHUB_TOKEN":        true,
}

// testEnvironmentDenied reports whether a test script must not inherit a
// variable. Beyond the exact names, ACTIONS_* carries the runtime, results,
// cache, and OIDC endpoints and their tokens; INPUT_* holds action inputs;
// BUILDER_* is reserved for the script contract; and any name that looks like
// a credential is dropped.
func testEnvironmentDenied(name string) bool {
	upper := strings.ToUpper(name)
	if testEnvironmentDeniedNames[upper] {
		return true
	}
	for _, prefix := range []string{"ACTIONS_", "INPUT_", "BUILDER_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "PRIVATE_KEY", "CREDENTIAL", "AGE_IDENTITY"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// TestEnvironment is the environment of a test script: the runner's own, so
// Xcode, simulators, Homebrew, and language toolchains behave as in any macOS
// job, minus every variable that could publish data or act with runner or
// repository credentials, plus the BUILDER_* contract variables.
func TestEnvironment(base []string, sourceRoot, iosPath, reportDir string) []string {
	env := make([]string, 0, len(base)+3)
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if !testEnvironmentDenied(name) {
			env = append(env, entry)
		}
	}
	return append(env,
		"BUILDER_SOURCE_DIR="+sourceRoot,
		"BUILDER_IOS_PATH="+iosPath,
		"BUILDER_REPORT_DIR="+reportDir,
	)
}

func resetPrivateDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear private report directory")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("create private report directory")
	}
	return nil
}

// stageReport copies at most limit bytes of report.md out of the script's
// report directory to destination. The report is optional, so a missing one
// is not an error. note, for the private log, describes a report that was
// ignored or truncated.
func stageReport(reportDir, destination string, limit int64) (note string, err error) {
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("clear staged report")
	}
	dirInfo, err := os.Lstat(reportDir)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil || !dirInfo.IsDir() {
		return "The report directory was replaced, so report.md was ignored.", nil
	}
	reportPath := filepath.Join(reportDir, testReportName)
	info, err := os.Lstat(reportPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return "report.md is not a regular file and was ignored.", nil
	}
	source, err := os.Open(reportPath)
	if err != nil {
		return "", fmt.Errorf("open report")
	}
	defer source.Close()
	staged, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("stage report")
	}
	_, copyErr := io.Copy(staged, io.LimitReader(source, limit))
	truncated := false
	if copyErr == nil {
		var probe [1]byte
		if n, _ := source.Read(probe[:]); n > 0 {
			truncated = true
			_, copyErr = fmt.Fprintf(staged, "\n\n> Builder truncated this report at %d bytes.\n", limit)
		}
	}
	if closeErr := staged.Close(); copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return "", fmt.Errorf("stage report")
	}
	if truncated {
		return fmt.Sprintf("report.md exceeded %d bytes and was truncated.", limit), nil
	}
	return "", nil
}

// boundTestLog keeps the log at path within limit bytes by replacing its
// middle with a note, so the head (setup) and the tail (the test summary) both
// survive.
func boundTestLog(path string, limit, head int64) (retErr error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("inspect private test log")
	}
	if info.Size() <= limit {
		return nil
	}
	tail := limit - head - testLogNoteSpace
	if head <= 0 || tail <= 0 {
		return fmt.Errorf("invalid log bounds")
	}
	source, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open private test log")
	}
	defer source.Close()
	temporary := path + ".bounded"
	_ = os.Remove(temporary)
	bounded, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create bounded test log")
	}
	defer func() {
		_ = bounded.Close()
		if retErr != nil {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := io.CopyN(bounded, source, head); err != nil {
		return fmt.Errorf("copy test log head")
	}
	omitted := info.Size() - head - tail
	if _, err := fmt.Fprintf(bounded, "\n\n[Builder omitted %d bytes of test output here to keep the encrypted log under %d MiB.]\n\n", omitted, limit>>20); err != nil {
		return fmt.Errorf("write test log note")
	}
	if _, err := source.Seek(info.Size()-tail, io.SeekStart); err != nil {
		return fmt.Errorf("seek test log tail")
	}
	if _, err := io.CopyN(bounded, source, tail); err != nil {
		return fmt.Errorf("copy test log tail")
	}
	if err := bounded.Close(); err != nil {
		return fmt.Errorf("finish bounded test log")
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace test log")
	}
	return nil
}

func appendPrivateLog(path, message string) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(message)
}
