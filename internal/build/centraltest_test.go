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
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/runner"
)

func zipReader(t *testing.T, data []byte) *zip.Reader {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

// fakeRunStore stands in for the GitHub API after a run has completed.
type fakeRunStore struct {
	archive       []byte
	digest        string // "" means the correct digest; "none" means no digest
	findErr       error
	downloadErr   error
	deleteArtErr  error
	deleteRunErrs []error // returned in turn; nil once exhausted
	calls         []string
}

func (s *fakeRunStore) PollForRunArtifact(_ context.Context, owner, repo string, runID int64, name string, _ time.Duration) (*github.Artifact, error) {
	s.calls = append(s.calls, fmt.Sprintf("find %s/%s %d %s", owner, repo, runID, name))
	if s.findErr != nil {
		return nil, s.findErr
	}
	digest := s.digest
	switch digest {
	case "":
		sum := sha256.Sum256(s.archive)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	case "none":
		digest = ""
	}
	return &github.Artifact{ID: 77, Name: name, SizeInBytes: int64(len(s.archive)), Digest: digest}, nil
}

func (s *fakeRunStore) DownloadArtifactTo(_ context.Context, _, _ string, artifactID, maxBytes int64, w io.Writer, progress github.ProgressFunc) (int64, error) {
	s.calls = append(s.calls, fmt.Sprintf("download %d", artifactID))
	if s.downloadErr != nil {
		return 0, s.downloadErr
	}
	if int64(len(s.archive)) > maxBytes {
		return 0, errors.New("too large")
	}
	n, err := w.Write(s.archive)
	if progress != nil {
		progress(int64(n), int64(len(s.archive)))
	}
	return int64(n), err
}

func (s *fakeRunStore) DeleteArtifact(_ context.Context, owner, repo string, artifactID int64) error {
	s.calls = append(s.calls, fmt.Sprintf("delete artifact %s/%s %d", owner, repo, artifactID))
	return s.deleteArtErr
}

func (s *fakeRunStore) DeleteWorkflowRun(_ context.Context, owner, repo string, runID int64) error {
	s.calls = append(s.calls, fmt.Sprintf("delete run %s/%s %d", owner, repo, runID))
	if len(s.deleteRunErrs) == 0 {
		return nil
	}
	err := s.deleteRunErrs[0]
	s.deleteRunErrs = s.deleteRunErrs[1:]
	return err
}

func (s *fakeRunStore) deleted() bool {
	for _, call := range s.calls {
		if strings.HasPrefix(call, "delete") {
			return true
		}
	}
	return false
}

type finishHarness struct {
	identity *age.X25519Identity
	output   string
	progress *bytes.Buffer
	c        *Coordinator
}

const finishBuildID = "123e4567-e89b-42d3-a456-426614174000"

func newFinishHarness(t *testing.T) *finishHarness {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Project:  "App",
		Backend:  config.BackendCentral,
		GitHub:   config.GitHubConfig{Owner: "source", Repo: "private"},
		Builder:  config.BuilderConfig{Owner: "builder", Repo: "public", Workflow: "ios-build.yml"},
		Security: config.SecurityConfig{Recipient: identity.Recipient().String()},
	}
	var progress bytes.Buffer
	return &finishHarness{identity: identity, output: t.TempDir(), progress: &progress, c: NewCoordinatorWithOutput(cfg, nil, &progress)}
}

//nolint:gocritic // opts is copied so shared option values stay unchanged.
func (h *finishHarness) finish(t *testing.T, store *fakeRunStore, conclusion string, opts TestOptions) (*TestResult, error) {
	t.Helper()
	opts.OutputDir = h.output
	if opts.Platform == "" {
		opts.Platform = PlatformIOS
	}
	run := &github.WorkflowRun{ID: 42, Status: "completed", Conclusion: conclusion, HTMLURL: "https://github.com/builder/public/actions/runs/42"}
	result := &TestResult{BuildID: finishBuildID, Platform: opts.Platform}
	err := h.c.finishTest(context.Background(), store, run, &opts, h.identity, result)
	return result, err
}

func (h *finishHarness) archive(t *testing.T, members map[string]string) []byte {
	t.Helper()
	files := map[string][]byte{}
	for name, plaintext := range members {
		files[name] = encryptForTest(t, h.identity, plaintext)
	}
	return makeZIP(t, files)
}

var windowsArtifactOptions = TestOptions{Platform: PlatformWindows, Script: "scripts/windows-test.ps1", Artifact: "dist/EntrixSetup.exe"}

func TestFinishTestDeletesTheRunOnlyAfterAPass(t *testing.T) {
	t.Run("ios pass", func(t *testing.T) {
		h := newFinishHarness(t)
		store := &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "log", centralReportFile: "# ok"})}
		result, err := h.finish(t, store, "success", TestOptions{Script: "scripts/test.sh"})
		if err != nil || !result.Passed || !result.RunDeleted || result.RunKept != "" {
			t.Fatalf("result = %+v, %v", result, err)
		}
		want := []string{
			"find builder/public 42 ios-builder-" + finishBuildID, "download 77",
			"delete artifact builder/public 77", "delete run builder/public 42",
		}
		if !reflect.DeepEqual(store.calls, want) {
			t.Fatalf("calls = %q, want %q", store.calls, want)
		}
		if got := readFile(t, filepath.Join(h.output, "ios-test-"+finishBuildID+".md")); got != "# ok" {
			t.Fatalf("report = %q", got)
		}
		assertNoDownloadLeftovers(t, h.output)
	})
	t.Run("windows pass with artifact", func(t *testing.T) {
		h := newFinishHarness(t)
		store := &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "log", centralArtifactFile: "MZ-installer"})}
		result, err := h.finish(t, store, "success", windowsArtifactOptions)
		if err != nil || !result.Passed || !result.RunDeleted {
			t.Fatalf("result = %+v, %v", result, err)
		}
		if result.ArtifactPath != filepath.Join(h.output, "EntrixSetup.exe") || result.ArtifactSize != int64(len("MZ-installer")) {
			t.Fatalf("artifact = %q (%d bytes)", result.ArtifactPath, result.ArtifactSize)
		}
		if got := readFile(t, result.ArtifactPath); got != "MZ-installer" {
			t.Fatalf("artifact contents = %q", got)
		}
		if info, err := os.Stat(result.ArtifactPath); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
			t.Fatalf("artifact mode = %v, %v", info, err)
		}
		if result.LogPath != filepath.Join(h.output, "windows-test-"+finishBuildID+".log") {
			t.Fatalf("log path = %q", result.LogPath)
		}
		assertNoDownloadLeftovers(t, h.output)
	})
	t.Run("keep-run keeps a pass", func(t *testing.T) {
		h := newFinishHarness(t)
		store := &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "log", centralArtifactFile: "MZ"})}
		opts := windowsArtifactOptions
		opts.KeepRun = true
		result, err := h.finish(t, store, "success", opts)
		if err != nil || !result.Passed || result.RunDeleted || result.RunKept != RunKeptOnRequest || store.deleted() {
			t.Fatalf("result = %+v, %v, calls %q", result, err, store.calls)
		}
	})
}

func TestFinishTestKeepsTheRunWhenAnythingFailed(t *testing.T) {
	for _, test := range []struct {
		name       string
		conclusion string
		options    TestOptions
		store      func(*finishHarness, *testing.T) *fakeRunStore
		wantErr    string // "" means the tests failed without an error
	}{
		{"script failed", "failure", windowsArtifactOptions, func(h *finishHarness, t *testing.T) *fakeRunStore {
			return &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "exit 1", centralReportFile: "# failed"})}
		}, ""},
		{"ios script failed", "failure", TestOptions{Script: "scripts/test.sh"}, func(h *finishHarness, t *testing.T) *fakeRunStore {
			return &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "exit 1"})}
		}, ""},
		{"job timed out", "timed_out", windowsArtifactOptions, func(h *finishHarness, t *testing.T) *fakeRunStore {
			return &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "The test script timed out"})}
		}, ""},
		{"run cancelled", "cancelled", windowsArtifactOptions, func(h *finishHarness, t *testing.T) *fakeRunStore {
			return &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "log"})}
		}, ""},
		{"failed run with an artifact is never trusted", "failure", windowsArtifactOptions, func(h *finishHarness, t *testing.T) *fakeRunStore {
			return &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "log", centralArtifactFile: "MZ"})}
		}, ""},
		{"successful run without artifact.age", "success", windowsArtifactOptions, func(h *finishHarness, t *testing.T) *fakeRunStore {
			return &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "log"})}
		}, "uploaded no artifact.age"},
		{"no uploaded artifact", "failure", windowsArtifactOptions, func(*finishHarness, *testing.T) *fakeRunStore {
			return &fakeRunStore{findErr: github.ErrArtifactNotFound}
		}, "without an encrypted test artifact"},
		{"download error", "success", windowsArtifactOptions, func(*finishHarness, *testing.T) *fakeRunStore {
			return &fakeRunStore{archive: []byte("zip"), downloadErr: errors.New("connection reset")}
		}, "connection reset"},
		{"digest mismatch", "success", windowsArtifactOptions, func(h *finishHarness, t *testing.T) *fakeRunStore {
			return &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "log", centralArtifactFile: "MZ"}), digest: "sha256:" + strings.Repeat("0", 64)}
		}, "digest does not match"},
		{"artifact for another identity", "success", windowsArtifactOptions, func(h *finishHarness, t *testing.T) *fakeRunStore {
			other, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			return &fakeRunStore{archive: makeZIP(t, map[string][]byte{
				centralTestLogFile:  encryptForTest(t, h.identity, "log"),
				centralArtifactFile: encryptForTest(t, other, "MZ"),
			})}
		}, "decrypt artifact"},
		{"log for another identity", "success", TestOptions{Script: "scripts/test.sh"}, func(_ *finishHarness, t *testing.T) *fakeRunStore {
			other, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			return &fakeRunStore{archive: makeZIP(t, map[string][]byte{centralTestLogFile: encryptForTest(t, other, "log")})}
		}, "decrypt test log"},
		{"artifact.age in an iOS run", "success", TestOptions{Script: "scripts/test.sh"}, func(h *finishHarness, t *testing.T) *fakeRunStore {
			return &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "log", centralArtifactFile: "MZ"})}
		}, "unexpected artifact ZIP member"},
		{"not a ZIP", "success", windowsArtifactOptions, func(*finishHarness, *testing.T) *fakeRunStore {
			return &fakeRunStore{archive: []byte("plaintext, not a zip")}
		}, "open artifact ZIP"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newFinishHarness(t)
			store := test.store(h, t)
			result, err := h.finish(t, store, test.conclusion, test.options)
			if test.wantErr == "" {
				if err != nil || result.Passed || result.RunKept != RunKeptFailed {
					t.Fatalf("result = %+v, %v; want failed tests and a kept run", result, err)
				}
				if result.LogPath == "" || readFile(t, result.LogPath) == "" {
					t.Fatal("the failing run's log was not saved")
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantErr) || !strings.Contains(err.Error(), "run kept: https://github.com/builder/public/actions/runs/42") ||
				result.Passed || result.RunKept != RunKeptNotRetrieved {
				t.Fatalf("result = %+v, error %v; want an error containing %q that names the kept run", result, err, test.wantErr)
			}
			if store.deleted() {
				t.Fatalf("a run whose tests did not pass completely was deleted: %q", store.calls)
			}
			if result.RunDeleted || result.ArtifactPath != "" {
				t.Fatalf("result = %+v", result)
			}
			if _, err := os.Stat(filepath.Join(h.output, "EntrixSetup.exe")); !os.IsNotExist(err) {
				t.Fatal("an artifact was written for a run that did not pass")
			}
			assertNoDownloadLeftovers(t, h.output)
		})
	}
}

func TestFinishTestRetriesABusyRunAndWarnsOnFailure(t *testing.T) {
	previous := runDeleteRetryDelay
	runDeleteRetryDelay = time.Millisecond
	defer func() { runDeleteRetryDelay = previous }()
	busy := fmt.Errorf("%w (status 409): Cannot delete a workflow run that is not completed", github.ErrWorkflowRunBusy)

	t.Run("busy, then deleted", func(t *testing.T) {
		h := newFinishHarness(t)
		store := &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "log"}), deleteRunErrs: []error{busy, busy}}
		result, err := h.finish(t, store, "success", TestOptions{Script: "scripts/test.sh"})
		if err != nil || !result.RunDeleted || strings.Count(strings.Join(store.calls, "\n"), "delete run") != 3 {
			t.Fatalf("result = %+v, %v, calls %q", result, err, store.calls)
		}
	})
	t.Run("no permission", func(t *testing.T) {
		h := newFinishHarness(t)
		forbidden := errors.New("failed to delete workflow run (status 403): Resource not accessible by personal access token")
		store := &fakeRunStore{
			archive:       h.archive(t, map[string]string{centralTestLogFile: "log", centralArtifactFile: "MZ"}),
			deleteArtErr:  errors.New("failed to delete artifact (status 403)"),
			deleteRunErrs: []error{forbidden, forbidden},
		}
		result, err := h.finish(t, store, "success", windowsArtifactOptions)
		if err != nil || !result.Passed || result.RunDeleted || result.RunKept != RunKeptDeleteFailure || result.ArtifactPath == "" {
			t.Fatalf("a deletion failure must leave a passing result: %+v, %v", result, err)
		}
		if strings.Count(strings.Join(store.calls, "\n"), "delete run") != 1 {
			t.Fatalf("a permission error was retried: %q", store.calls)
		}
		for _, want := range []string{
			"could not delete encrypted artifact 77",
			"could not delete workflow run https://github.com/builder/public/actions/runs/42: failed to delete workflow run (status 403)",
			"repo scope", "gh auth refresh -h github.com -s repo,workflow", "Actions: Read and write on builder/public",
		} {
			if !strings.Contains(h.progress.String(), want) {
				t.Errorf("warning missing %q:\n%s", want, h.progress.String())
			}
		}
	})
	t.Run("busy until the retries run out", func(t *testing.T) {
		h := newFinishHarness(t)
		store := &fakeRunStore{archive: h.archive(t, map[string]string{centralTestLogFile: "log"}), deleteRunErrs: []error{busy, busy, busy, busy, busy, busy, busy}}
		result, err := h.finish(t, store, "success", TestOptions{Script: "scripts/test.sh"})
		if err != nil || !result.Passed || result.RunDeleted || strings.Count(strings.Join(store.calls, "\n"), "delete run") != runDeleteAttempts {
			t.Fatalf("result = %+v, %v, calls %q", result, err, store.calls)
		}
	})
}

func TestDecryptZipMemberToFileStreamsAndCaps(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "artifact.zip")
	const size = 64 << 20
	func() {
		file, err := os.Create(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		zw := zip.NewWriter(file)
		member, err := zw.CreateHeader(&zip.FileHeader{Name: centralArtifactFile, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		encrypted, err := age.Encrypt(member, identity.Recipient())
		if err != nil {
			t.Fatal(err)
		}
		chunk := bytes.Repeat([]byte("0123456789abcdef"), 4096)
		for written := 0; written < size; written += len(chunk) {
			if _, err := encrypted.Write(chunk); err != nil {
				t.Fatal(err)
			}
		}
		if err := encrypted.Close(); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.Close() }()
	destination := filepath.Join(dir, "out", "Setup.exe")
	var written int64
	peak := peakHeapDuring(func() {
		written, err = decryptZipMemberToFile(identity, archive.File[0], destination, MaxTestArtifactSize)
	})
	if err != nil || written != size {
		t.Fatalf("decryptZipMemberToFile() = %d, %v", written, err)
	}
	if peak > 24<<20 {
		t.Fatalf("decrypting a %d MiB artifact held %d MiB of heap; it must stream", size>>20, peak>>20)
	}
	if info, err := os.Stat(destination); err != nil || info.Size() != size {
		t.Fatalf("decrypted artifact = %v, %v", info, err)
	}

	// Over the cap: nothing replaces the destination, and no temporary file stays.
	if _, err := decryptZipMemberToFile(identity, archive.File[0], filepath.Join(dir, "capped", "Setup.exe"), size-1); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("an artifact over the cap: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "capped")); err != nil || len(entries) != 0 {
		t.Fatalf("capped output directory = %v, %v", entries, err)
	}
}

func TestTestArtifactMembersAllowArtifactOnlyForWindows(t *testing.T) {
	archive := zipReader(t, makeZIP(t, map[string][]byte{centralTestLogFile: []byte("c"), centralArtifactFile: []byte("c")}))
	if _, err := testArtifactMembers(archive, false); err == nil {
		t.Fatal("artifact.age accepted in an iOS test artifact")
	}
	if members, err := testArtifactMembers(archive, true); err != nil || members[centralArtifactFile] == nil {
		t.Fatalf("testArtifactMembers(windows) = %v, %v", members, err)
	}
	for name, files := range map[string]map[string][]byte{
		"plaintext exe":   {centralTestLogFile: []byte("c"), "EntrixSetup.exe": []byte("MZ")},
		"nested artifact": {centralTestLogFile: []byte("c"), "dist/" + centralArtifactFile: []byte("c")},
		"no log":          {centralArtifactFile: []byte("c")},
	} {
		if _, err := testArtifactMembers(zipReader(t, makeZIP(t, files)), true); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestCentralWindowsTestDispatchInputs(t *testing.T) {
	cfg := &config.Config{
		GitHub:   config.GitHubConfig{Owner: "source-owner", Repo: "private-app"},
		IOS:      config.IOSConfig{Path: "ios", Scheme: "App", Configuration: "Debug", TestScript: "scripts/ios.sh"},
		Windows:  config.WindowsConfig{TestScript: "scripts/from-config.ps1", Artifact: "dist/from-config.exe"},
		Security: config.SecurityConfig{Recipient: " age1example "},
	}
	want := map[string]string{
		"build_id":           finishBuildID,
		"source_owner":       "source-owner",
		"source_repo":        "private-app",
		"snapshot_ref":       "refs/ios-builder/jobs/" + finishBuildID,
		"artifact_recipient": "age1example",
		"operation":          "windows-test",
		"test_script":        "scripts/windows-test.ps1",
		"artifact_path":      "dist/EntrixSetup.exe",
	}
	got := centralWindowsTestDispatchInputs(cfg, finishBuildID, want["snapshot_ref"], "scripts/windows-test.ps1", "dist/EntrixSetup.exe")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("centralWindowsTestDispatchInputs() = %#v, want %#v", got, want)
	}
	if _, present := centralWindowsTestDispatchInputs(cfg, finishBuildID, want["snapshot_ref"], "t.ps1", "")["artifact_path"]; present {
		t.Fatal("an empty artifact was dispatched")
	}
}

func TestTestValidatesPlatformOptions(t *testing.T) {
	h := newFinishHarness(t)
	for name, opts := range map[string]TestOptions{
		"windows .cmd script":      {Platform: PlatformWindows, Script: "scripts/test.cmd"},
		"windows hostile script":   {Platform: PlatformWindows, Script: "../test.ps1"},
		"windows hostile artifact": {Platform: PlatformWindows, Script: "scripts/test.ps1", Artifact: "../EntrixSetup.exe"},
		"ios artifact":             {Platform: PlatformIOS, Script: "scripts/test.sh", Artifact: "dist/App.ipa"},
		"unknown platform":         {Platform: "android", Script: "scripts/test.sh"},
	} {
		if _, err := h.c.Test(context.Background(), opts); err == nil {
			t.Errorf("%s: Test() accepted invalid options", name)
		}
	}
}

func TestLimitsMatchTheRunner(t *testing.T) {
	if MaxTestArtifactSize != runner.MaxTestArtifactBytes {
		t.Fatalf("CLI artifact limit %d != runner limit %d", MaxTestArtifactSize, runner.MaxTestArtifactBytes)
	}
	if maxReportSize < runner.MaxTestReportBytes {
		t.Fatalf("CLI report limit %d < runner limit %d", maxReportSize, runner.MaxTestReportBytes)
	}
	// The whole Windows artifact ZIP (artifact, log, report) fits the archive limit.
	if maxArtifactCiphertextSize+maxLogCiphertextSize+maxReportSize+1<<20 > maxArtifactArchiveSize {
		t.Fatal("a maximal Windows test artifact exceeds the archive limit")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertNoDownloadLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".ios-builder-") {
			t.Errorf("temporary file %s left in the output directory", entry.Name())
		}
	}
}

// peakHeapDuring runs fn while sampling the live heap and returns the most it
// held above the level before fn. Streaming code keeps this small however much
// garbage it produces; reading a whole file into memory does not.
func peakHeapDuring(fn func()) uint64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	base, peak := stats.HeapAlloc, stats.HeapAlloc
	done := make(chan struct{})
	sampled := make(chan uint64)
	go func() {
		for {
			var sample runtime.MemStats
			runtime.ReadMemStats(&sample)
			if sample.HeapAlloc > peak {
				peak = sample.HeapAlloc
			}
			select {
			case <-done:
				sampled <- peak
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	fn()
	close(done)
	if max := <-sampled; max > base {
		return max - base
	}
	return 0
}
