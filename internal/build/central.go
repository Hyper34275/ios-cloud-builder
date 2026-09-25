package build

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/MobAI-App/ios-builder/internal/security"
	"github.com/MobAI-App/ios-builder/internal/snapshot"
	"github.com/google/uuid"
	"howett.net/plist"
)

const (
	centralArtifactPrefix = "ios-builder-"
	centralDeployPrefix   = "ios-builder-deploy-"
	centralIPAFile        = "App.ipa.age"
	centralLogFile        = "build.log.age"
	centralTestLogFile    = "test.log.age"
	centralReportFile     = "report.md.age"
	centralArtifactFile   = "artifact.age"

	maxArtifactArchiveSize = int64(1024*1024*1024 + 80*1024*1024)
	maxIPACiphertextSize   = int64(1024 * 1024 * 1024)
	maxLogCiphertextSize   = int64(64 * 1024 * 1024)
	// The runner caps report.md at 1 MiB and may append a truncation note.
	maxReportSize        = int64(2 * 1024 * 1024)
	maxIPAEntries        = 100000
	maxInfoPlistSize     = int64(4 * 1024 * 1024)
	cleanupTimeout       = 30 * time.Second
	artifactIndexTimeout = 30 * time.Second
)

type encryptedArtifact struct {
	ipa []byte
	log []byte
}

// Diagnostics are the decrypted files retrieved for one central run.
type Diagnostics struct {
	LogPath    string
	ReportPath string // set only for a test run whose script wrote report.md
}

func (c *Coordinator) buildCentral(parent context.Context, opts BuildOptions) (*BuildResult, error) {
	started := time.Now()
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(parent, opts.Timeout)
	defer cancel()

	buildID := uuid.NewString()
	result := &BuildResult{BuildID: buildID, TestFlight: opts.TestFlight}
	c.progress.Start(buildID)

	if err := c.config.Validate(); err != nil {
		return result, err
	}
	identity, err := centralIdentity(c.config)
	if err != nil {
		return result, err
	}
	if err := snapshot.VerifyRemote(ctx, opts.Remote, c.config.GitHub.Owner, c.config.GitHub.Repo); err != nil {
		return result, fmt.Errorf("verify private source remote: %w", err)
	}

	owner, repo := c.config.Builder.Owner, c.config.Builder.Repo
	inputs := centralDispatchInputs(c.config, buildID, snapshot.Ref(buildID), opts.TestFlight)
	run, err := c.awaitCentralRun(ctx, opts.Remote, buildID, inputs, opts.Timeout, PhaseBuilding, "Building securely on the central runner...")
	if run != nil {
		result.WorkflowURL = run.HTMLURL
	}
	if err != nil {
		return result, err
	}

	baseArtifact, err := c.github.PollForRunArtifact(ctx, owner, repo, run.ID, centralArtifactPrefix+buildID, artifactIndexTimeout)
	if err != nil {
		c.progress.Error(PhaseBuilding, err)
		return result, fmt.Errorf("encrypted build artifact unavailable: %w", err)
	}
	defer c.deleteCentralArtifact(owner, repo, baseArtifact.ID)
	artifact := baseArtifact
	if opts.TestFlight {
		deployName := centralDeployPrefix + buildID
		if run.Conclusion == "success" {
			artifact, err = c.github.PollForRunArtifact(ctx, owner, repo, run.ID, deployName, artifactIndexTimeout)
			if err != nil {
				return result, fmt.Errorf("encrypted TestFlight diagnostic unavailable: %w", err)
			}
		} else if deployArtifact, findErr := c.github.FindArtifactByName(ctx, owner, repo, run.ID, deployName); findErr == nil {
			artifact = deployArtifact
		}
		if artifact.ID != baseArtifact.ID {
			defer c.deleteCentralArtifact(owner, repo, artifact.ID)
		}
	}

	c.progress.Update(PhaseDownloading, "Downloading encrypted build artifact...")
	contents, err := c.downloadCentralArtifact(ctx, owner, repo, artifact)
	if err != nil {
		c.progress.Error(PhaseDownloading, err)
		return result, err
	}

	if run.Conclusion != "success" {
		logPath, logErr := decryptLogToFile(identity, contents.log, opts.OutputDir, buildID)
		if logErr != nil {
			c.progress.Error(PhaseBuilding, logErr)
			return result, fmt.Errorf("central workflow concluded %s; decrypt diagnostics: %w", run.Conclusion, logErr)
		}
		result.LogPath = logPath
		result.Duration = time.Since(started)
		c.progress.Error(PhaseBuilding, fmt.Errorf("workflow concluded %s", run.Conclusion))
		kind := "build"
		if opts.TestFlight {
			kind = "TestFlight deployment"
		}
		return result, fmt.Errorf("%s failed with conclusion %s; decrypted diagnostics: %s", kind, run.Conclusion, logPath)
	}
	if opts.TestFlight {
		result.Duration = time.Since(started)
		c.progress.Complete(PhaseBuilding, "Signed upload accepted by App Store Connect")
		c.progress.Finish()
		return result, nil
	}
	if len(contents.ipa) == 0 {
		return result, errors.New("successful central artifact is missing App.ipa.age")
	}
	plaintextIPA, err := decryptBounded(identity, contents.ipa, maxIPACiphertextSize)
	if err != nil {
		return result, fmt.Errorf("decrypt IPA: %w", err)
	}
	if err := validateIPA(plaintextIPA); err != nil {
		return result, fmt.Errorf("validate decrypted IPA: %w", err)
	}
	ipaPath := filepath.Join(opts.OutputDir, centralOutputName(c.config.Project, "", ".ipa"))
	if err := atomicWritePrivate(ipaPath, plaintextIPA); err != nil {
		return result, fmt.Errorf("save decrypted IPA: %w", err)
	}

	result.IPAPath = ipaPath
	result.IPASize = int64(len(plaintextIPA))
	result.Duration = time.Since(started)
	c.progress.Complete(PhaseBuilding, "Build completed successfully")
	c.progress.Complete(PhaseDownloading, fmt.Sprintf("IPA decrypted (%.2f MB)", float64(result.IPASize)/(1024*1024)))
	c.progress.Finish()
	return result, nil
}

// awaitCentralRun snapshots the working tree to the build's private ref,
// dispatches the central workflow with inputs, and waits for the run to
// complete, reporting progress under phase. Before it returns, the snapshot
// ref is deleted and a run that did not complete is cancelled. The returned
// run is non-nil once the run has started, even alongside an error.
func (c *Coordinator) awaitCentralRun(ctx context.Context, remote, buildID string, inputs map[string]string, timeout time.Duration, phase Phase, running string) (*github.WorkflowRun, error) {
	c.progress.Update(PhaseSnapshot, "Snapshotting working tree...")
	sha, err := snapshot.Create(ctx, fmt.Sprintf("ios-builder snapshot %s", buildID))
	if err != nil {
		c.progress.Error(PhaseSnapshot, err)
		return nil, fmt.Errorf("failed to snapshot working tree: %w", err)
	}
	ref := snapshot.Ref(buildID)
	if inputs["snapshot_ref"] != ref {
		return nil, errors.New("dispatch inputs do not name this build's snapshot ref")
	}
	if err := snapshot.Push(ctx, remote, sha, ref); err != nil {
		c.progress.Error(PhaseSnapshot, err)
		return nil, fmt.Errorf("failed to push private snapshot: %w", err)
	}
	defer c.deleteSnapshotLease(remote, ref, sha)
	c.progress.Complete(PhaseSnapshot, fmt.Sprintf("Pushed %s", sha[:7]))

	owner, repo, workflow := c.config.Builder.Owner, c.config.Builder.Repo, c.config.Builder.Workflow
	c.progress.Update(PhaseTriggering, "Triggering central GitHub Actions workflow...")
	if err := c.github.TriggerWorkflow(ctx, owner, repo, workflow, inputs); err != nil {
		c.progress.Error(PhaseTriggering, err)
		return nil, fmt.Errorf("failed to trigger central workflow: %w", err)
	}
	c.progress.Complete(PhaseTriggering, "Workflow triggered")
	var runID int64
	runCompleted := false
	defer func() {
		if runCompleted {
			return
		}
		if runID != 0 {
			c.cancelCentralRun(owner, repo, runID)
			return
		}
		c.cancelCentralRunByBuildID(owner, repo, workflow, buildID)
	}()

	c.progress.Update(PhaseWaitingStart, "Waiting for workflow to start...")
	started, err := c.github.PollForWorkflowStart(ctx, owner, repo, workflow, buildID, 2*time.Minute)
	if err != nil {
		c.progress.Error(PhaseWaitingStart, err)
		return nil, fmt.Errorf("central workflow failed to start: %w", err)
	}
	runID = started.ID
	c.progress.SetWorkflowURL(started.HTMLURL)
	c.progress.Complete(PhaseWaitingStart, fmt.Sprintf("Workflow started (run #%d)", started.ID))

	c.progress.Update(phase, running)
	completed, err := c.github.PollForWorkflowCompletion(ctx, owner, repo, started.ID, timeout, func() {
		c.showCentralRunningStep(ctx, owner, repo, started.ID)
	})
	if err != nil {
		c.progress.Error(phase, err)
		return started, fmt.Errorf("central workflow did not complete: %w", err)
	}
	runCompleted = true
	if completed.HTMLURL == "" {
		completed.HTMLURL = started.HTMLURL
	}
	return completed, nil
}

// DownloadLogs retrieves and decrypts the diagnostic log for an exact central
// build ID and returns its path. See DownloadDiagnostics.
func (c *Coordinator) DownloadLogs(ctx context.Context, buildID, outputDir string) (string, error) {
	diagnostics, err := c.DownloadDiagnostics(ctx, buildID, outputDir)
	if err != nil {
		return "", err
	}
	return diagnostics.LogPath, nil
}

// DownloadDiagnostics retrieves and decrypts the diagnostics for an exact
// central build ID: the build or deployment log, or a test run's log and
// report (an iOS or Windows run; a Windows run's artifact is not restored).
// It is used by `builder ios logs <build-id>` and deliberately does not
// support repository-backend artifacts, which are plaintext upstream.
func (c *Coordinator) DownloadDiagnostics(ctx context.Context, buildID, outputDir string) (*Diagnostics, error) {
	if !c.config.IsCentral() {
		return nil, errors.New("encrypted build logs are available only for the central backend")
	}
	if err := c.config.Validate(); err != nil {
		return nil, err
	}
	parsed, err := uuid.Parse(buildID)
	if err != nil || parsed.String() != buildID || parsed.Version() != 4 {
		return nil, errors.New("build ID must be a canonical lowercase UUIDv4")
	}
	identity, err := centralIdentity(c.config)
	if err != nil {
		return nil, err
	}
	owner, repo, workflow := c.config.Builder.Owner, c.config.Builder.Repo, c.config.Builder.Workflow
	run, err := c.github.FindWorkflowRunByBuildID(ctx, owner, repo, workflow, buildID)
	if err != nil {
		return nil, err
	}
	artifact, err := c.github.FindArtifactByName(ctx, owner, repo, run.ID, centralDeployPrefix+buildID)
	if err != nil {
		artifact, err = c.github.FindArtifactByName(ctx, owner, repo, run.ID, centralArtifactPrefix+buildID)
	}
	if err != nil {
		return nil, err
	}
	archivePath, err := c.downloadCentralArtifactFile(ctx, c.github, owner, repo, artifact, outputDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(archivePath) }()
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open artifact ZIP: %w", err)
	}
	defer func() { _ = archive.Close() }()
	platform := PlatformIOS
	if run.DisplayTitle == github.WindowsTestRunTitlePrefix+buildID {
		platform = PlatformWindows
	}
	diagnostics, err := saveDiagnostics(identity, &archive.Reader, outputDir, buildID, platform)
	if err != nil {
		return nil, err
	}
	c.deleteCentralArtifact(owner, repo, artifact.ID)
	return diagnostics, nil
}

// saveDiagnostics decrypts a downloaded artifact's diagnostics into
// outputDir. A test run's artifact carries test.log.age; every other central
// artifact carries build.log.age. A Windows run's artifact.age is left alone.
func saveDiagnostics(identity age.Identity, archive *zip.Reader, outputDir, buildID string, platform TestPlatform) (*Diagnostics, error) {
	if isTestArtifact(archive) {
		members, err := testArtifactMembers(archive, true)
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
		return &saved.Diagnostics, nil
	}
	members, err := ciphertextMembers(archive, map[string]int64{
		centralIPAFile: maxIPACiphertextSize,
		centralLogFile: maxLogCiphertextSize,
	}, centralLogFile)
	if err != nil {
		return nil, err
	}
	log, err := readBoundedZipFile(members[centralLogFile], maxLogCiphertextSize)
	if err != nil {
		return nil, err
	}
	path, err := decryptLogToFile(identity, log, outputDir, buildID)
	if err != nil {
		return nil, err
	}
	return &Diagnostics{LogPath: path}, nil
}

func centralIdentity(cfg *config.Config) (*age.X25519Identity, error) {
	store, err := security.NewIdentityStore()
	if err != nil {
		return nil, fmt.Errorf("open local AGE identity store: %w", err)
	}
	identity, err := store.Identity()
	if err != nil {
		return nil, fmt.Errorf("load local AGE identity (run `builder security init` first): %w", err)
	}
	if identity.Recipient().String() != strings.TrimSpace(cfg.Security.Recipient) {
		return nil, errors.New("local AGE identity does not match security.recipient in builder.json")
	}
	return identity, nil
}

func centralDispatchInputs(cfg *config.Config, buildID, ref string, testFlight bool) map[string]string {
	inputs := map[string]string{
		"build_id":           buildID,
		"source_owner":       cfg.GitHub.Owner,
		"source_repo":        cfg.GitHub.Repo,
		"snapshot_ref":       ref,
		"ios_path":           ".",
		"framework_hint":     frameworkHint(cfg),
		"artifact_recipient": strings.TrimSpace(cfg.Security.Recipient),
		"operation":          "build",
	}
	if cfg.IOS.Path != "" {
		inputs["ios_path"] = cfg.IOS.Path
	}
	if cfg.IOS.Scheme != "" {
		inputs["scheme"] = cfg.IOS.Scheme
	}
	if testFlight {
		inputs["operation"] = "testflight"
		inputs["configuration"] = "Release"
	} else if cfg.IOS.Configuration != "" {
		inputs["configuration"] = cfg.IOS.Configuration
	}
	return inputs
}

func frameworkHint(cfg *config.Config) string {
	switch {
	case cfg.ReactNative.Expo:
		return "expo"
	case cfg.Flutter.Version != "":
		return "flutter"
	case cfg.KMP.JDKVersion != "":
		return "kmp"
	default:
		return "auto"
	}
}

func (c *Coordinator) downloadCentralArtifact(ctx context.Context, owner, repo string, artifact *github.Artifact) (*encryptedArtifact, error) {
	data, err := c.downloadCentralArtifactData(ctx, owner, repo, artifact)
	if err != nil {
		return nil, err
	}
	return parseEncryptedArtifact(data)
}

// downloadCentralArtifactData downloads an artifact ZIP within the size
// bound and verifies it against GitHub's digest, without interpreting it.
func (c *Coordinator) downloadCentralArtifactData(ctx context.Context, owner, repo string, artifact *github.Artifact) ([]byte, error) {
	if artifact.SizeInBytes > maxArtifactArchiveSize {
		return nil, fmt.Errorf("encrypted artifact metadata exceeds %d byte limit", maxArtifactArchiveSize)
	}
	data, err := c.github.DownloadArtifactWithProgressLimit(ctx, owner, repo, artifact.ID, maxArtifactArchiveSize, func(downloaded, total int64) {
		c.progress.UpdateDownloadProgress(downloaded, total)
	})
	if err != nil {
		return nil, fmt.Errorf("download encrypted artifact: %w", err)
	}
	if err := verifyArtifactDigest(artifact.Digest, data); err != nil {
		return nil, err
	}
	return data, nil
}

func verifyArtifactDigest(digest string, data []byte) error {
	actual := sha256.Sum256(data)
	return verifyArtifactDigestSum(digest, actual[:])
}

// verifyArtifactDigestSum compares GitHub's "sha256:<hex>" artifact digest
// with the SHA-256 of the downloaded archive. An artifact without a digest is
// accepted, as older artifacts have none.
func verifyArtifactDigestSum(digest string, actual []byte) error {
	if digest == "" {
		return nil
	}
	algorithm, encoded, ok := strings.Cut(digest, ":")
	if !ok || algorithm != "sha256" || len(encoded) != sha256.Size*2 {
		return fmt.Errorf("artifact API returned malformed SHA-256 digest")
	}
	expected, err := hex.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("artifact API returned malformed SHA-256 digest")
	}
	if !bytes.Equal(expected, actual) {
		return errors.New("downloaded artifact SHA-256 digest does not match GitHub metadata")
	}
	return nil
}

func parseEncryptedArtifact(data []byte) (*encryptedArtifact, error) {
	members, err := parseCiphertextArtifact(data, map[string]int64{
		centralIPAFile: maxIPACiphertextSize,
		centralLogFile: maxLogCiphertextSize,
	}, centralLogFile)
	if err != nil {
		return nil, err
	}
	return &encryptedArtifact{ipa: members[centralIPAFile], log: members[centralLogFile]}, nil
}

// parseCiphertextArtifact reads an artifact ZIP that may contain only the
// allowed members, each once and within its size limit, and must contain the
// required one.
func parseCiphertextArtifact(data []byte, allowed map[string]int64, required string) (map[string][]byte, error) {
	if int64(len(data)) > maxArtifactArchiveSize {
		return nil, fmt.Errorf("artifact archive exceeds %d byte limit", maxArtifactArchiveSize)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open artifact ZIP: %w", err)
	}
	files, err := ciphertextMembers(zr, allowed, required)
	if err != nil {
		return nil, err
	}
	members := make(map[string][]byte, len(files))
	for name, file := range files {
		if members[name], err = readBoundedZipFile(file, allowed[name]); err != nil {
			return nil, err
		}
	}
	return members, nil
}

// ciphertextMembers checks, without reading any member, that an artifact ZIP
// contains only the allowed members, each once as a regular file within its
// size limit, including the required one.
func ciphertextMembers(zr *zip.Reader, allowed map[string]int64, required string) (map[string]*zip.File, error) {
	if len(zr.File) == 0 || len(zr.File) > len(allowed) {
		return nil, fmt.Errorf("artifact ZIP must contain one to %d ciphertext files, got %d", len(allowed), len(zr.File))
	}
	members := make(map[string]*zip.File, len(zr.File))
	for _, file := range zr.File {
		if file.FileInfo().IsDir() {
			return nil, fmt.Errorf("unexpected directory %q in artifact ZIP", file.Name)
		}
		limit, ok := allowed[file.Name]
		if !ok {
			return nil, fmt.Errorf("unexpected artifact ZIP member %q", file.Name)
		}
		if _, duplicate := members[file.Name]; duplicate {
			return nil, fmt.Errorf("duplicate %s in artifact ZIP", file.Name)
		}
		if !file.Mode().IsRegular() {
			return nil, fmt.Errorf("artifact member %q is not a regular file", file.Name)
		}
		if file.UncompressedSize64 == 0 || file.UncompressedSize64 > uint64(limit) {
			return nil, fmt.Errorf("artifact member %q is empty or exceeds %d byte limit", file.Name, limit)
		}
		members[file.Name] = file
	}
	if members[required] == nil {
		return nil, fmt.Errorf("artifact ZIP is missing %s", required)
	}
	return members, nil
}

func readBoundedZipFile(file *zip.File, limit int64) ([]byte, error) {
	if !file.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact member %q is not a regular file", file.Name)
	}
	if file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("artifact member %q exceeds %d byte limit", file.Name, limit)
	}
	r, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open artifact member %q: %w", file.Name, err)
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read artifact member %q: %w", file.Name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("artifact member %q exceeds %d byte limit", file.Name, limit)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("artifact member %q is empty", file.Name)
	}
	return data, nil
}

func validateIPA(data []byte) error {
	if int64(len(data)) > maxIPACiphertextSize {
		return fmt.Errorf("IPA exceeds %d byte limit", maxIPACiphertextSize)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open IPA ZIP: %w", err)
	}
	if len(zr.File) == 0 || len(zr.File) > maxIPAEntries {
		return fmt.Errorf("invalid IPA ZIP entry count %d", len(zr.File))
	}
	apps := make(map[string]bool)
	files := make(map[string]*zip.File, len(zr.File))
	for _, file := range zr.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		cleanName := strings.TrimSuffix(name, "/")
		if name != file.Name || strings.HasPrefix(name, "/") || cleanName == "" || path.Clean(cleanName) != cleanName {
			return fmt.Errorf("unsafe IPA ZIP member %q", file.Name)
		}
		if _, exists := files[name]; exists {
			return fmt.Errorf("duplicate IPA ZIP member %q", file.Name)
		}
		files[name] = file
		if strings.HasPrefix(name, "Payload/") {
			rest := strings.TrimPrefix(name, "Payload/")
			part := strings.SplitN(rest, "/", 2)[0]
			if strings.HasSuffix(part, ".app") && part != ".app" {
				apps["Payload/"+part] = true
			}
		}
	}
	if len(apps) != 1 {
		return fmt.Errorf("IPA must contain exactly one Payload/*.app, got %d", len(apps))
	}
	var appPath string
	for path := range apps {
		appPath = path
	}
	infoFile := files[appPath+"/Info.plist"]
	if infoFile == nil || infoFile.FileInfo().IsDir() {
		return errors.New("IPA app is missing Info.plist")
	}
	infoData, err := readBoundedZipFile(infoFile, maxInfoPlistSize)
	if err != nil {
		return err
	}
	var info struct {
		Executable string `plist:"CFBundleExecutable"`
	}
	if _, err := plist.Unmarshal(infoData, &info); err != nil {
		return fmt.Errorf("parse app Info.plist: %w", err)
	}
	if info.Executable == "" || filepath.Base(info.Executable) != info.Executable {
		return errors.New("invalid CFBundleExecutable in Info.plist")
	}
	executable := files[appPath+"/"+info.Executable]
	if executable == nil || !executable.Mode().IsRegular() || executable.UncompressedSize64 == 0 {
		return fmt.Errorf("IPA app executable %q is missing or empty", info.Executable)
	}
	return nil
}

func decryptLogToFile(identity age.Identity, ciphertext []byte, outputDir, buildID string) (string, error) {
	if len(ciphertext) == 0 {
		return "", errors.New("encrypted diagnostic log is missing")
	}
	plaintext, err := decryptBounded(identity, ciphertext, maxLogCiphertextSize)
	if err != nil {
		return "", fmt.Errorf("decrypt build log: %w", err)
	}
	path := filepath.Join(outputDir, centralOutputName("ios-builder", buildID, ".log"))
	if err := atomicWritePrivate(path, plaintext); err != nil {
		return "", fmt.Errorf("save decrypted build log: %w", err)
	}
	return path, nil
}

func decryptBounded(identity age.Identity, ciphertext []byte, limit int64) ([]byte, error) {
	reader, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("initialize AGE decryption: %w", err)
	}
	plaintext, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("decrypt data: %w", err)
	}
	if int64(len(plaintext)) > limit {
		return nil, fmt.Errorf("decrypted data exceeds %d byte limit", limit)
	}
	return plaintext, nil
}

func centralOutputName(project, buildID, extension string) string {
	project = strings.TrimSpace(project)
	project = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, project)
	project = strings.Trim(project, ".-")
	if project == "" {
		project = "App"
	}
	if buildID == "" {
		return project + extension
	}
	return project + "-" + buildID + extension
}

func atomicWritePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ios-builder-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (c *Coordinator) showCentralRunningStep(ctx context.Context, owner, repo string, runID int64) {
	step, total, err := c.github.RunningStep(ctx, owner, repo, runID)
	if err == nil && step != nil {
		c.progress.UpdateStep(step.Name, step.Number, total, time.Since(step.StartedAt))
	}
}

func (c *Coordinator) deleteSnapshotLease(remote, ref, sha string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := snapshot.DeleteLease(ctx, remote, ref, sha); err != nil {
		c.progress.Warn(fmt.Sprintf("could not delete snapshot ref %s: %v", ref, err))
	}
}

func (c *Coordinator) cancelCentralRun(owner, repo string, runID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := c.github.CancelWorkflowRun(ctx, owner, repo, runID); err != nil {
		c.progress.Warn(fmt.Sprintf("could not cancel workflow run %d: %v", runID, err))
	}
}

func (c *Coordinator) cancelCentralRunByBuildID(owner, repo, workflow, buildID string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	run, err := c.github.PollForWorkflowStart(ctx, owner, repo, workflow, buildID, cleanupTimeout)
	if err != nil {
		c.progress.Warn(fmt.Sprintf("could not locate dispatched workflow %s for cancellation: %v", buildID, err))
		return
	}
	if err := c.github.CancelWorkflowRun(ctx, owner, repo, run.ID); err != nil {
		c.progress.Warn(fmt.Sprintf("could not cancel workflow run %d: %v", run.ID, err))
	}
}

func (c *Coordinator) deleteCentralArtifact(owner, repo string, artifactID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := c.github.DeleteArtifact(ctx, owner, repo, artifactID); err != nil {
		c.progress.Warn(fmt.Sprintf("could not delete encrypted artifact %d: %v", artifactID, err))
	}
}
