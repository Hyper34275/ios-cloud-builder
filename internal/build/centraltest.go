package build

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/snapshot"
	"github.com/google/uuid"
)

// TestPlatform selects the central builder's test job.
type TestPlatform string

const (
	// PlatformIOS runs the test script on macOS with Xcode (`builder ios test`).
	PlatformIOS TestPlatform = "ios"
	// PlatformWindows runs the test script on Windows (`builder windows test`).
	PlatformWindows TestPlatform = "windows"
)

const (
	// DefaultTestTimeout is how long `builder ios test` waits by default,
	// including queueing; the workflow's test job itself is limited to 120 minutes.
	DefaultTestTimeout = 2 * time.Hour
	// DefaultWindowsTestTimeout is how long `builder windows test` waits by
	// default, including queueing; the windows-test job is limited to 150 minutes.
	DefaultWindowsTestTimeout = 150 * time.Minute

	// MaxTestArtifactSize is the largest artifact a Windows test run returns.
	// It matches the runner's limit.
	MaxTestArtifactSize = int64(1 << 30)
	// AGE adds 16 bytes per 64 KiB chunk and a short header.
	maxArtifactCiphertextSize = MaxTestArtifactSize + 1<<20

	// testRetrievalTimeout is the download and decryption budget of a finished
	// run. It is separate from --timeout, so a run that took most of it still
	// has its results retrieved.
	testRetrievalTimeout = 30 * time.Minute

	// A finished run may briefly refuse deletion while GitHub completes it.
	runDeleteAttempts = 6
	runDeleteTimeout  = 2 * time.Minute
)

// runDeleteRetryDelay is a variable so tests need not wait.
var runDeleteRetryDelay = 5 * time.Second

func (platform TestPlatform) valid() bool {
	return platform == PlatformIOS || platform == PlatformWindows
}

// title names the platform in progress output.
func (platform TestPlatform) title() string {
	if platform == PlatformWindows {
		return "Windows"
	}
	return "iOS"
}

// outputPrefix names the decrypted log and report: ios-test-<id>.log.
func (platform TestPlatform) outputPrefix() string {
	return string(platform) + "-test"
}

// TestOptions controls a central test run.
type TestOptions struct {
	OutputDir string
	Timeout   time.Duration
	Remote    string       // Git remote to push the working-tree snapshot to
	Script    string       // test script, relative to the repository root
	Platform  TestPlatform // PlatformIOS when empty
	// Artifact is, for a Windows run only, the file a passing script builds,
	// relative to the repository root. It is decrypted to OutputDir under its
	// base name.
	Artifact string
	// KeepRun keeps the public run and its encrypted artifacts even when the
	// tests pass. A run whose tests did not pass is always kept.
	KeepRun bool
}

// TestResult describes a completed central test run. Passed is false when the
// script failed or did not produce its artifact; its decrypted log and report
// are saved either way.
type TestResult struct {
	BuildID      string
	Platform     TestPlatform
	Passed       bool
	Conclusion   string
	LogPath      string
	ReportPath   string // empty when the script wrote no report.md
	Report       []byte // decrypted report.md
	ArtifactPath string // the decrypted artifact, after a pass that requested one
	ArtifactSize int64
	Duration     time.Duration
	WorkflowURL  string
	// RunDeleted reports that the run and its encrypted artifacts were
	// deleted from the public builder. Otherwise RunKept says why they remain.
	RunDeleted bool
	RunKept    string
}

// Why a finished run stays in the public builder.
const (
	RunKeptFailed        = "the tests did not pass"
	RunKeptNotRetrieved  = "its results could not be retrieved"
	RunKeptOnRequest     = "--keep-run was given"
	RunKeptDeleteFailure = "it could not be deleted"
)

// testRunStore is the part of the GitHub API a finished test run needs.
// *github.Client implements it; tests substitute a fake.
type testRunStore interface {
	PollForRunArtifact(ctx context.Context, owner, repo string, runID int64, artifactName string, timeout time.Duration) (*github.Artifact, error)
	DownloadArtifactTo(ctx context.Context, owner, repo string, artifactID, maxBytes int64, w io.Writer, progress github.ProgressFunc) (int64, error)
	DeleteArtifact(ctx context.Context, owner, repo string, artifactID int64) error
	DeleteWorkflowRun(ctx context.Context, owner, repo string, runID int64) error
}

type encryptedTestArtifact struct {
	log    []byte
	report []byte
}

type savedTestOutputs struct {
	Diagnostics
	report       []byte
	artifactPath string
	artifactSize int64
}

// Test runs the project's test script on the central builder and retrieves
// its encrypted log, optional report, and, for a passing Windows run, its
// artifact. Failing tests are not an error: the result has Passed false and
// the decrypted diagnostics are saved either way.
//
// Only a run whose tests passed, whose outputs were all retrieved, and whose
// options do not say KeepRun is deleted from the public builder, with its
// artifacts. Any other finished run is kept, so its encrypted outputs can be
// retrieved again while they last. The private snapshot ref is always deleted.
//
//nolint:gocritic // opts is a value, like BuildOptions, so filled-in defaults never reach the caller.
func (c *Coordinator) Test(parent context.Context, opts TestOptions) (*TestResult, error) {
	started := time.Now()
	if opts.Platform == "" {
		opts.Platform = PlatformIOS
	}
	if !opts.Platform.valid() {
		return nil, fmt.Errorf("unknown test platform %q", opts.Platform)
	}
	command := "builder " + string(opts.Platform) + " test"
	if !c.config.IsCentral() {
		return nil, fmt.Errorf("`%s` requires backend=central", command)
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTestTimeout
		if opts.Platform == PlatformWindows {
			opts.Timeout = DefaultWindowsTestTimeout
		}
	}
	ctx, cancel := context.WithTimeout(parent, opts.Timeout)
	defer cancel()

	buildID := uuid.NewString()
	result := &TestResult{BuildID: buildID, Platform: opts.Platform}
	c.progress.StartPlatformOperation(buildID, opts.Platform.title(), "Tests")

	if err := c.config.Validate(); err != nil {
		return result, err
	}
	if err := validateTestOptions(&opts); err != nil {
		return result, err
	}
	identity, err := centralIdentity(c.config)
	if err != nil {
		return result, err
	}
	if err := snapshot.VerifyRemote(ctx, opts.Remote, c.config.GitHub.Owner, c.config.GitHub.Repo); err != nil {
		return result, fmt.Errorf("verify private source remote: %w", err)
	}

	inputs := centralTestDispatchInputs(c.config, buildID, snapshot.Ref(buildID), opts.Script)
	if opts.Platform == PlatformWindows {
		inputs = centralWindowsTestDispatchInputs(c.config, buildID, snapshot.Ref(buildID), opts.Script, opts.Artifact)
	}
	running := fmt.Sprintf("Running tests securely on the central %s runner...", opts.Platform.title())
	run, err := c.awaitCentralRun(ctx, opts.Remote, buildID, inputs, opts.Timeout, PhaseTesting, running)
	if run != nil {
		result.WorkflowURL = run.HTMLURL
	}
	if err != nil {
		if run != nil {
			// A run that did not complete was cancelled, never deleted.
			result.RunKept = RunKeptNotRetrieved
			return result, fmt.Errorf("%w (run kept: %s)", err, run.HTMLURL)
		}
		return result, err
	}
	retrieveCtx, cancelRetrieve := context.WithTimeout(parent, testRetrievalTimeout)
	defer cancelRetrieve()
	err = c.finishTest(retrieveCtx, c.github, run, &opts, identity, result)
	result.Duration = time.Since(started)
	return result, err
}

func validateTestOptions(opts *TestOptions) error {
	if opts.Platform == PlatformWindows {
		if err := config.ValidateWindowsTestScriptPath(opts.Script); err != nil {
			return fmt.Errorf("invalid test script: %w", err)
		}
		if opts.Artifact != "" {
			if err := config.ValidateArtifactPath(opts.Artifact); err != nil {
				return fmt.Errorf("invalid artifact: %w", err)
			}
		}
		return nil
	}
	if err := config.ValidateTestScriptPath(opts.Script); err != nil {
		return fmt.Errorf("invalid test script: %w", err)
	}
	if opts.Artifact != "" {
		return errors.New("an artifact is supported only for Windows test runs")
	}
	return nil
}

// finishTest retrieves a completed run's encrypted outputs into result, then
// deletes the run only if the tests passed completely. An error means the
// outputs could not all be retrieved; the run is then kept.
func (c *Coordinator) finishTest(ctx context.Context, store testRunStore, run *github.WorkflowRun, opts *TestOptions, identity age.Identity, result *TestResult) error {
	owner, repo := c.config.Builder.Owner, c.config.Builder.Repo
	result.Conclusion = run.Conclusion
	result.RunKept = RunKeptNotRetrieved
	artifact, err := store.PollForRunArtifact(ctx, owner, repo, run.ID, centralArtifactPrefix+result.BuildID, artifactIndexTimeout)
	if err != nil {
		c.progress.Error(PhaseTesting, err)
		return fmt.Errorf("workflow concluded %s without an encrypted test artifact (run kept: %s): %w", run.Conclusion, run.HTMLURL, err)
	}

	c.progress.Update(PhaseDownloading, "Downloading encrypted test results...")
	artifactDestination := ""
	if opts.Artifact != "" {
		artifactDestination = filepath.Join(opts.OutputDir, path.Base(opts.Artifact))
	}
	succeeded := run.Conclusion == "success"
	saved, err := c.retrieveTestArtifact(ctx, store, owner, repo, artifact, identity, opts.OutputDir, result.BuildID, opts.Platform, artifactDestination, succeeded)
	if saved != nil {
		result.LogPath, result.ReportPath, result.Report = saved.LogPath, saved.ReportPath, saved.report
		result.ArtifactPath, result.ArtifactSize = saved.artifactPath, saved.artifactSize
	}
	if err != nil {
		c.progress.Error(PhaseDownloading, err)
		return fmt.Errorf("retrieve the encrypted test results (run kept: %s): %w", run.HTMLURL, err)
	}
	result.Passed = succeeded && (opts.Artifact == "" || result.ArtifactPath != "")
	decrypted := "Log and report decrypted"
	if result.ArtifactPath != "" {
		decrypted = fmt.Sprintf("Log, report and %s decrypted (%.1f MB)", filepath.Base(result.ArtifactPath), float64(result.ArtifactSize)/(1024*1024))
	}
	if !result.Passed {
		result.RunKept = RunKeptFailed
		c.progress.Complete(PhaseDownloading, decrypted)
		c.progress.Error(PhaseTesting, fmt.Errorf("tests failed (workflow concluded %s)", run.Conclusion))
		return nil
	}
	c.progress.Complete(PhaseTesting, "Tests passed")
	c.progress.Complete(PhaseDownloading, decrypted)
	switch {
	case opts.KeepRun:
		result.RunKept = RunKeptOnRequest
	case c.deleteFinishedRun(store, owner, repo, run, artifact.ID):
		result.RunDeleted, result.RunKept = true, ""
	default:
		result.RunKept = RunKeptDeleteFailure
	}
	c.progress.Finish()
	return nil
}

// retrieveTestArtifact downloads a test run's artifact ZIP to a temporary
// file beside the outputs, decrypts the log and report, and, when the run
// succeeded and artifactDestination is set, streams the decrypted artifact
// there. The returned outputs are what was saved even alongside an error.
func (c *Coordinator) retrieveTestArtifact(ctx context.Context, store testRunStore, owner, repo string, artifact *github.Artifact,
	identity age.Identity, outputDir, buildID string, platform TestPlatform, artifactDestination string, succeeded bool) (*savedTestOutputs, error) {
	archivePath, err := c.downloadCentralArtifactFile(ctx, store, owner, repo, artifact, outputDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(archivePath) }()
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open artifact ZIP: %w", err)
	}
	// Closed before the deferred removal: Windows cannot delete an open file.
	defer func() { _ = archive.Close() }()
	members, err := testArtifactMembers(&archive.Reader, artifactDestination != "")
	if err != nil {
		return nil, err
	}
	contents, err := readTestLogAndReport(members)
	if err != nil {
		return nil, err
	}
	saved, err := saveTestOutputs(identity, contents, outputDir, buildID, platform)
	if err != nil {
		return nil, err
	}
	// A failed run never has an artifact to trust, whatever its ZIP holds.
	if artifactDestination == "" || !succeeded {
		return saved, nil
	}
	file := members[centralArtifactFile]
	if file == nil {
		return saved, errors.New("the run succeeded but uploaded no artifact.age")
	}
	size, err := decryptZipMemberToFile(identity, file, artifactDestination, MaxTestArtifactSize)
	if err != nil {
		return saved, fmt.Errorf("decrypt artifact: %w", err)
	}
	saved.artifactPath, saved.artifactSize = artifactDestination, size
	return saved, nil
}

// downloadCentralArtifactFile streams an artifact ZIP within the size bound
// into a private temporary file in dir, verifies it against GitHub's digest,
// and returns its path. The caller removes the file.
func (c *Coordinator) downloadCentralArtifactFile(ctx context.Context, store testRunStore, owner, repo string, artifact *github.Artifact, dir string) (string, error) {
	if artifact.SizeInBytes > maxArtifactArchiveSize {
		return "", fmt.Errorf("encrypted artifact metadata exceeds %d byte limit", maxArtifactArchiveSize)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".ios-builder-download-*.zip")
	if err != nil {
		return "", fmt.Errorf("create temporary artifact file: %w", err)
	}
	name := file.Name()
	digest := sha256.New()
	_, downloadErr := store.DownloadArtifactTo(ctx, owner, repo, artifact.ID, maxArtifactArchiveSize, io.MultiWriter(file, digest), func(downloaded, total int64) {
		c.progress.UpdateDownloadProgress(downloaded, total)
	})
	closeErr := file.Close()
	switch {
	case downloadErr != nil:
		_ = os.Remove(name)
		return "", fmt.Errorf("download encrypted artifact: %w", downloadErr)
	case closeErr != nil:
		_ = os.Remove(name)
		return "", fmt.Errorf("save encrypted artifact: %w", closeErr)
	}
	if err := verifyArtifactDigestSum(artifact.Digest, digest.Sum(nil)); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// testArtifactMembers accepts exactly a test run's ciphertext: the mandatory
// test.log.age, an optional report.md.age and, when allowArtifact, an optional
// artifact.age.
func testArtifactMembers(archive *zip.Reader, allowArtifact bool) (map[string]*zip.File, error) {
	allowed := map[string]int64{
		centralTestLogFile: maxLogCiphertextSize,
		centralReportFile:  maxReportSize,
	}
	if allowArtifact {
		allowed[centralArtifactFile] = maxArtifactCiphertextSize
	}
	return ciphertextMembers(archive, allowed, centralTestLogFile)
}

func readTestLogAndReport(members map[string]*zip.File) (*encryptedTestArtifact, error) {
	log, err := readBoundedZipFile(members[centralTestLogFile], maxLogCiphertextSize)
	if err != nil {
		return nil, err
	}
	contents := &encryptedTestArtifact{log: log}
	if report := members[centralReportFile]; report != nil {
		if contents.report, err = readBoundedZipFile(report, maxReportSize); err != nil {
			return nil, err
		}
	}
	return contents, nil
}

// parseEncryptedTestArtifact reads an in-memory iOS test artifact ZIP.
func parseEncryptedTestArtifact(data []byte) (*encryptedTestArtifact, error) {
	members, err := parseCiphertextArtifact(data, map[string]int64{
		centralTestLogFile: maxLogCiphertextSize,
		centralReportFile:  maxReportSize,
	}, centralTestLogFile)
	if err != nil {
		return nil, err
	}
	return &encryptedTestArtifact{log: members[centralTestLogFile], report: members[centralReportFile]}, nil
}

// isTestArtifact reports whether an artifact ZIP holds a test run's log.
func isTestArtifact(archive *zip.Reader) bool {
	for _, file := range archive.File {
		if file.Name == centralTestLogFile {
			return true
		}
	}
	return false
}

// saveTestOutputs decrypts a test run's log to <out>/<platform>-test-<id>.log
// and, when present, its report to <out>/<platform>-test-<id>.md.
func saveTestOutputs(identity age.Identity, contents *encryptedTestArtifact, outputDir, buildID string, platform TestPlatform) (*savedTestOutputs, error) {
	if len(contents.log) == 0 {
		return nil, errors.New("encrypted test log is missing")
	}
	log, err := decryptBounded(identity, contents.log, maxLogCiphertextSize)
	if err != nil {
		return nil, fmt.Errorf("decrypt test log: %w", err)
	}
	saved := &savedTestOutputs{}
	saved.LogPath = filepath.Join(outputDir, centralOutputName(platform.outputPrefix(), buildID, ".log"))
	if err := atomicWritePrivate(saved.LogPath, log); err != nil {
		return nil, fmt.Errorf("save decrypted test log: %w", err)
	}
	if len(contents.report) == 0 {
		return saved, nil
	}
	report, err := decryptBounded(identity, contents.report, maxReportSize)
	if err != nil {
		return nil, fmt.Errorf("decrypt test report: %w", err)
	}
	saved.ReportPath = filepath.Join(outputDir, centralOutputName(platform.outputPrefix(), buildID, ".md"))
	if err := atomicWritePrivate(saved.ReportPath, report); err != nil {
		return nil, fmt.Errorf("save decrypted test report: %w", err)
	}
	saved.report = report
	return saved, nil
}

// decryptZipMemberToFile streams one AGE-encrypted ZIP member through
// decryption into a mode-0600 file that replaces destination only once the
// whole plaintext is authenticated, and returns the plaintext size. At most
// limit plaintext bytes are accepted; memory use does not grow with the file.
func decryptZipMemberToFile(identity age.Identity, file *zip.File, destination string, limit int64) (size int64, retErr error) {
	if !file.Mode().IsRegular() || file.UncompressedSize64 > uint64(limit+(1<<20)) {
		return 0, fmt.Errorf("artifact member %q is not a regular file within %d bytes", file.Name, limit)
	}
	member, err := file.Open()
	if err != nil {
		return 0, fmt.Errorf("open artifact member %q: %w", file.Name, err)
	}
	defer member.Close()
	plaintext, err := age.Decrypt(member, identity)
	if err != nil {
		return 0, fmt.Errorf("initialize AGE decryption: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return 0, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".ios-builder-*")
	if err != nil {
		return 0, err
	}
	defer func() {
		if retErr != nil {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return 0, err
	}
	size, err = io.Copy(temporary, io.LimitReader(plaintext, limit+1))
	switch {
	case err != nil:
		return 0, fmt.Errorf("decrypt data: %w", err)
	case size > limit:
		return 0, fmt.Errorf("decrypted artifact exceeds %d byte limit", limit)
	case size == 0:
		return 0, errors.New("decrypted artifact is empty")
	}
	if err := temporary.Sync(); err != nil {
		return 0, err
	}
	if err := temporary.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		return 0, err
	}
	return size, nil
}

// deleteFinishedRun deletes a run's artifact and then the run itself from the
// public builder, retrying briefly while GitHub still finishes the run. A
// failure is a warning, never an error: the outputs are already safe locally.
func (c *Coordinator) deleteFinishedRun(store testRunStore, owner, repo string, run *github.WorkflowRun, artifactID int64) bool {
	ctx, cancel := context.WithTimeout(context.Background(), runDeleteTimeout)
	defer cancel()
	if err := store.DeleteArtifact(ctx, owner, repo, artifactID); err != nil {
		c.progress.Warn(fmt.Sprintf("could not delete encrypted artifact %d: %v", artifactID, err))
	}
	var err error
	for attempt := 1; ; attempt++ {
		if err = store.DeleteWorkflowRun(ctx, owner, repo, run.ID); err == nil {
			return true
		}
		if !errors.Is(err, github.ErrWorkflowRunBusy) || attempt == runDeleteAttempts || !sleepContext(ctx, runDeleteRetryDelay) {
			break
		}
	}
	c.progress.Warn(runDeleteWarning(owner, repo, run, err))
	return false
}

// sleepContext waits for delay and reports false if ctx ended first.
func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func runDeleteWarning(owner, repo string, run *github.WorkflowRun, err error) string {
	location := run.HTMLURL
	if location == "" {
		location = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", owner, repo, run.ID)
	}
	var hint strings.Builder
	fmt.Fprintf(&hint, "could not delete workflow run %s: %v\n", location, err)
	fmt.Fprintf(&hint, "   The run stays in %s/%s until you delete it on that page (the ... menu, Delete workflow run).\n", owner, repo)
	fmt.Fprintf(&hint, "   Deleting runs needs write access to %s/%s and a token with the repo scope (builder auth github requests it;\n", owner, repo)
	fmt.Fprint(&hint, "   for the GitHub CLI run gh auth refresh -h github.com -s repo,workflow) or, for a fine-grained token,\n")
	fmt.Fprintf(&hint, "   Actions: Read and write on %s/%s. builder central doctor shows which token builder uses.", owner, repo)
	return hint.String()
}

// centralTestDispatchInputs sends only what the iOS test job reads. Scheme,
// configuration, and framework keep their workflow defaults, which exposes
// less project metadata in the public run.
func centralTestDispatchInputs(cfg *config.Config, buildID, ref, script string) map[string]string {
	inputs := map[string]string{
		"build_id":           buildID,
		"source_owner":       cfg.GitHub.Owner,
		"source_repo":        cfg.GitHub.Repo,
		"snapshot_ref":       ref,
		"ios_path":           ".",
		"artifact_recipient": strings.TrimSpace(cfg.Security.Recipient),
		"operation":          "test",
		"test_script":        script,
	}
	if cfg.IOS.Path != "" {
		inputs["ios_path"] = cfg.IOS.Path
	}
	return inputs
}

// centralWindowsTestDispatchInputs sends only what the windows-test job
// reads; every iOS input keeps its workflow default.
func centralWindowsTestDispatchInputs(cfg *config.Config, buildID, ref, script, artifact string) map[string]string {
	inputs := map[string]string{
		"build_id":           buildID,
		"source_owner":       cfg.GitHub.Owner,
		"source_repo":        cfg.GitHub.Repo,
		"snapshot_ref":       ref,
		"artifact_recipient": strings.TrimSpace(cfg.Security.Recipient),
		"operation":          "windows-test",
		"test_script":        script,
	}
	if artifact != "" {
		inputs["artifact_path"] = artifact
	}
	return inputs
}
