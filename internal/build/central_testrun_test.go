package build

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/MobAI-App/ios-builder/internal/config"
)

func TestCentralTestDispatchInputs(t *testing.T) {
	cfg := &config.Config{
		GitHub:   config.GitHubConfig{Owner: "source-owner", Repo: "private-app"},
		IOS:      config.IOSConfig{Path: "ios", Scheme: "App", Configuration: "Debug", TestScript: "scripts/from-config.sh"},
		Flutter:  config.FlutterConfig{Version: "3.24.0"},
		Security: config.SecurityConfig{Recipient: " age1example "},
	}
	buildID := "123e4567-e89b-42d3-a456-426614174000"
	want := map[string]string{
		"build_id":           buildID,
		"source_owner":       "source-owner",
		"source_repo":        "private-app",
		"snapshot_ref":       "refs/ios-builder/jobs/" + buildID,
		"ios_path":           "ios",
		"artifact_recipient": "age1example",
		"operation":          "test",
		"test_script":        "scripts/ios-test.sh",
	}
	got := centralTestDispatchInputs(cfg, buildID, want["snapshot_ref"], "scripts/ios-test.sh")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("centralTestDispatchInputs() = %#v, want %#v", got, want)
	}

	cfg.IOS.Path = ""
	if got := centralTestDispatchInputs(cfg, buildID, want["snapshot_ref"], "t.sh")["ios_path"]; got != "." {
		t.Fatalf("ios_path = %q, want .", got)
	}
	if build := centralDispatchInputs(cfg, buildID, want["snapshot_ref"], false); build["test_script"] != "" || build["operation"] != "build" {
		t.Fatalf("build dispatch carries test inputs: %#v", build)
	}
}

func encryptForTest(t *testing.T, identity *age.X25519Identity, plaintext string) []byte {
	t.Helper()
	var ciphertext bytes.Buffer
	w, err := age.Encrypt(&ciphertext, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(plaintext)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return ciphertext.Bytes()
}

func TestParseEncryptedTestArtifactExactMembers(t *testing.T) {
	both, err := parseEncryptedTestArtifact(makeZIP(t, map[string][]byte{
		centralTestLogFile: []byte("log-ciphertext"),
		centralReportFile:  []byte("report-ciphertext"),
	}))
	if err != nil || string(both.log) != "log-ciphertext" || string(both.report) != "report-ciphertext" {
		t.Fatalf("parseEncryptedTestArtifact(log+report) = %#v, %v", both, err)
	}
	logOnly, err := parseEncryptedTestArtifact(makeZIP(t, map[string][]byte{centralTestLogFile: []byte("log-ciphertext")}))
	if err != nil || len(logOnly.report) != 0 {
		t.Fatalf("parseEncryptedTestArtifact(log only) = %#v, %v", logOnly, err)
	}
	for name, members := range map[string]map[string][]byte{
		"plaintext report":  {centralTestLogFile: []byte("c"), "report.md": []byte("plaintext")},
		"plaintext log":     {"test.log": []byte("plaintext")},
		"build log":         {centralLogFile: []byte("c")},
		"ipa":               {centralTestLogFile: []byte("c"), centralIPAFile: []byte("c")},
		"missing log":       {centralReportFile: []byte("c")},
		"source file":       {centralTestLogFile: []byte("c"), "Sources/App.swift": []byte("private")},
		"oversize report":   {centralTestLogFile: []byte("c"), centralReportFile: bytes.Repeat([]byte("x"), int(maxReportSize)+1)},
		"too many members":  {centralTestLogFile: []byte("c"), centralReportFile: []byte("c"), "extra": []byte("c")},
		"nested log member": {"dir/" + centralTestLogFile: []byte("c")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseEncryptedTestArtifact(makeZIP(t, members)); err == nil {
				t.Fatal("parseEncryptedTestArtifact() accepted invalid members")
			}
		})
	}
	// A test artifact is never mistaken for a build artifact, or vice versa.
	if _, err := parseEncryptedArtifact(makeZIP(t, map[string][]byte{centralTestLogFile: []byte("c")})); err == nil {
		t.Fatal("parseEncryptedArtifact() accepted a test artifact")
	}
	if !isTestArtifact(zipReader(t, makeZIP(t, map[string][]byte{centralTestLogFile: []byte("c")}))) ||
		isTestArtifact(zipReader(t, makeZIP(t, map[string][]byte{centralLogFile: []byte("c")}))) {
		t.Fatal("isTestArtifact() misclassified an artifact")
	}
}

func TestSaveTestOutputsWritesPrivateLogAndReport(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	buildID := "123e4567-e89b-42d3-a456-426614174000"
	output := t.TempDir()
	saved, err := saveTestOutputs(identity, &encryptedTestArtifact{
		log:    encryptForTest(t, identity, "private test log"),
		report: encryptForTest(t, identity, "# Report\n"),
	}, output, buildID, PlatformIOS)
	if err != nil {
		t.Fatal(err)
	}
	if saved.LogPath != filepath.Join(output, "ios-test-"+buildID+".log") || saved.ReportPath != filepath.Join(output, "ios-test-"+buildID+".md") {
		t.Fatalf("paths = %q, %q", saved.LogPath, saved.ReportPath)
	}
	for path, want := range map[string]string{saved.LogPath: "private test log", saved.ReportPath: "# Report\n"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", path, data, err, want)
		}
		info, err := os.Stat(path)
		if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
			t.Fatalf("%s mode = %v, %v; want 0600", path, info, err)
		}
	}
	if string(saved.report) != "# Report\n" {
		t.Fatalf("report = %q", saved.report)
	}

	logOnly, err := saveTestOutputs(identity, &encryptedTestArtifact{log: encryptForTest(t, identity, "log")}, t.TempDir(), buildID, PlatformIOS)
	if err != nil || logOnly.ReportPath != "" || logOnly.report != nil {
		t.Fatalf("saveTestOutputs(log only) = %#v, %v", logOnly, err)
	}

	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveTestOutputs(other, &encryptedTestArtifact{log: encryptForTest(t, identity, "log")}, t.TempDir(), buildID, PlatformIOS); err == nil {
		t.Fatal("ciphertext for another recipient was accepted")
	}
}

func TestSaveDiagnosticsRecognizesTestAndBuildArtifacts(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	buildID := "123e4567-e89b-42d3-a456-426614174000"
	output := t.TempDir()
	testRun, err := saveDiagnostics(identity, zipReader(t, makeZIP(t, map[string][]byte{
		centralTestLogFile: encryptForTest(t, identity, "test log"),
		centralReportFile:  encryptForTest(t, identity, "report"),
	})), output, buildID, PlatformIOS)
	if err != nil || filepath.Base(testRun.LogPath) != "ios-test-"+buildID+".log" || filepath.Base(testRun.ReportPath) != "ios-test-"+buildID+".md" {
		t.Fatalf("saveDiagnostics(test) = %#v, %v", testRun, err)
	}
	// A Windows run's diagnostics are named for Windows; its artifact is not restored.
	windowsRun, err := saveDiagnostics(identity, zipReader(t, makeZIP(t, map[string][]byte{
		centralTestLogFile:  encryptForTest(t, identity, "test log"),
		centralArtifactFile: encryptForTest(t, identity, "MZ"),
	})), output, buildID, PlatformWindows)
	if err != nil || filepath.Base(windowsRun.LogPath) != "windows-test-"+buildID+".log" || windowsRun.ReportPath != "" {
		t.Fatalf("saveDiagnostics(windows test) = %#v, %v", windowsRun, err)
	}
	buildRun, err := saveDiagnostics(identity, zipReader(t, makeZIP(t, map[string][]byte{
		centralLogFile: encryptForTest(t, identity, "build log"),
	})), output, buildID, PlatformIOS)
	if err != nil || filepath.Base(buildRun.LogPath) != "ios-builder-"+buildID+".log" || buildRun.ReportPath != "" {
		t.Fatalf("saveDiagnostics(build) = %#v, %v", buildRun, err)
	}
}

func TestTestRequiresCentralBackendAndValidScript(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	repository := &config.Config{Project: "App", Backend: config.BackendRepository, GitHub: config.GitHubConfig{Owner: "o", Repo: "r"}}
	if _, err := NewCoordinatorWithOutput(repository, nil, &bytes.Buffer{}).Test(context.Background(), TestOptions{Script: "test.sh"}); err == nil ||
		!strings.Contains(err.Error(), "backend=central") {
		t.Fatalf("Test(repository backend) error = %v", err)
	}
	central := &config.Config{
		Project:  "App",
		Backend:  config.BackendCentral,
		GitHub:   config.GitHubConfig{Owner: "source", Repo: "private"},
		Builder:  config.BuilderConfig{Owner: "builder", Repo: "public", Workflow: "ios-build.yml"},
		Security: config.SecurityConfig{Recipient: identity.Recipient().String()},
	}
	for _, script := range []string{"", "../escape.sh", "/abs.sh", "./test.sh"} {
		if _, err := NewCoordinatorWithOutput(central, nil, &bytes.Buffer{}).Test(context.Background(), TestOptions{Script: script}); err == nil ||
			!strings.Contains(err.Error(), "invalid test script") {
			t.Errorf("Test(%q) error = %v", script, err)
		}
	}
}

func TestProgressNamesTheOperation(t *testing.T) {
	var build, tests bytes.Buffer
	buildProgress := NewProgress(&build)
	buildProgress.Start("id")
	buildProgress.Finish()
	testProgress := NewProgress(&tests)
	testProgress.StartOperation("id", "Tests")
	testProgress.Update(PhaseTesting, "Running")
	testProgress.UpdateStep("Run project tests", 9, 11, 0)
	testProgress.Finish()
	if !strings.Contains(build.String(), "Remote iOS Build") || !strings.Contains(build.String(), "Build complete!") {
		t.Fatalf("build progress = %q", build.String())
	}
	if !strings.Contains(tests.String(), "Remote iOS Tests") || !strings.Contains(tests.String(), "Tests complete!") ||
		!strings.Contains(tests.String(), "🧪  Test: Run project tests (9/11)") {
		t.Fatalf("test progress = %q", tests.String())
	}
}
