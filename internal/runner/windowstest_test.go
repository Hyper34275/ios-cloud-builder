package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/config"
)

// artifactPaths are shared with the CLI's validation through
// TestArtifactValidationMatchesConfig: both sides must agree.
var (
	goodArtifactPaths = []string{
		"dist/EntrixSetup.exe",
		"EntrixSetup.exe",
		"out/Release/x64/App+Tools_1.2.3.msi",
		".artifacts/setup.zip",
		strings.Repeat("a", MaxArtifactPathLength),
	}
	hostileArtifactPaths = append([]string{
		"dist/../../EntrixSetup.exe",
		`dist\EntrixSetup.exe`,
		"D:/a/_temp/private-output/test.log",
		"dist/EntrixSetup.exe:Zone.Identifier",
		"dist/Entrix Setup.exe",
		"dist/ENTRIX~1.EXE",
		"dist/EntrixSetup.exe.",
		".git/objects/pack/pack.idx",
		"--output=dist/x.exe",
		"dist/$(whoami).exe",
		"dist/%TEMP%.exe",
	}, hostileTestScriptPaths...)
)

func TestValidateArtifactPath(t *testing.T) {
	for _, value := range goodArtifactPaths {
		if err := ValidateArtifactPath(value); err != nil {
			t.Errorf("ValidateArtifactPath(%q) = %v, want nil", value, err)
		}
	}
	for _, value := range hostileArtifactPaths {
		err := ValidateArtifactPath(value)
		if err == nil {
			t.Errorf("ValidateArtifactPath(%q) accepted a hostile path", value)
			continue
		}
		if value != "" && strings.Contains(err.Error(), value) {
			t.Errorf("ValidateArtifactPath(%q) echoed the value in %q", value, err)
		}
	}
}

func TestArtifactValidationMatchesConfig(t *testing.T) {
	for _, value := range append(append([]string{}, goodArtifactPaths...), hostileArtifactPaths...) {
		if runnerErr, configErr := ValidateArtifactPath(value), config.ValidateArtifactPath(value); (runnerErr == nil) != (configErr == nil) {
			t.Errorf("artifact %q: runner error %v, config error %v; the CLI and workflow must agree", value, runnerErr, configErr)
		}
	}
	for _, value := range []string{"scripts/windows-test.ps1", "scripts/TEST.PS1", "ci/test.sh", "scripts/test", "scripts/test.cmd", "scripts/test.bat", "scripts/test.ps1x", "../x.ps1"} {
		if runnerErr, configErr := ValidateWindowsTestScriptPath(value), config.ValidateWindowsTestScriptPath(value); (runnerErr == nil) != (configErr == nil) {
			t.Errorf("Windows script %q: runner error %v, config error %v; the CLI and workflow must agree", value, runnerErr, configErr)
		}
	}
}

func TestInputsValidateWindowsTestOperation(t *testing.T) {
	in := validInputs(t)
	in.Operation = OperationWindowsTest
	in.TestScript = "scripts/windows-test.ps1"
	in.ArtifactPath = "dist/EntrixSetup.exe"
	// The workflow defaults of the iOS-only inputs are accepted as they are.
	in.IOSPath, in.Scheme, in.Configuration, in.FrameworkHint = ".", "", "Release", FrameworkAuto
	if err := in.Validate(); err != nil {
		t.Fatalf("valid windows-test inputs rejected: %v", err)
	}
	for _, script := range []string{"scripts/windows-test.ps1", "Scripts/Test.PS1", "ci/test.sh"} {
		ok := in
		ok.TestScript = script
		if err := ok.Validate(); err != nil {
			t.Errorf("windows-test script %q rejected: %v", script, err)
		}
	}
	noArtifact := in
	noArtifact.ArtifactPath = ""
	if err := noArtifact.Validate(); err != nil {
		t.Errorf("windows-test without artifact_path rejected: %v", err)
	}
	for _, script := range append([]string{"scripts/test", "scripts/test.cmd", "scripts/test.bat", "scripts/test.exe", "scripts/test.ps1.txt"}, hostileTestScriptPaths...) {
		hostile := in
		hostile.TestScript = script
		if err := hostile.Validate(); err == nil {
			t.Errorf("windows-test script %q accepted", script)
		}
	}
	for _, artifact := range hostileArtifactPaths {
		if artifact == "" {
			continue // empty means no artifact
		}
		hostile := in
		hostile.ArtifactPath = artifact
		if err := hostile.Validate(); err == nil {
			t.Errorf("artifact_path %q accepted", artifact)
		}
	}
	// artifact_path belongs to windows-test alone.
	for _, operation := range []string{OperationBuild, OperationTestFlight, OperationTest} {
		other := in
		other.Operation = operation
		other.TestScript = ""
		if operation == OperationTest {
			other.TestScript = "scripts/test.sh"
		}
		if err := other.Validate(); err == nil || !strings.Contains(err.Error(), "artifact_path") {
			t.Errorf("operation %s accepted artifact_path: %v", operation, err)
		}
	}
}

func TestTestEnvironmentOmitsIOSPathForWindowsAndKeepsWindowsEntries(t *testing.T) {
	base := []string{
		`=C:=C:\work`,
		`Path=C:\Windows\System32;C:\Program Files\Git\bin`,
		`ProgramFiles=C:\Program Files`,
		`RUNNER_TEMP=D:\a\_temp`,
		`RUNNER_TRACKING_ID=github_00000000-0000-0000-0000-000000000000`,
		`GITHUB_WORKSPACE=D:\a\builder\builder`,
		`GITHUB_EVENT_PATH=D:\a\_temp\_github_workflow\event.json`,
		`github_output=D:\a\_temp\_runner_file_commands\set_output_1`,
		`Github_Step_Summary=D:\a\_temp\_runner_file_commands\step_summary_1`,
		`Actions_Runtime_Token=runtime-secret`,
		`ACTIONS_RUNNER_ACTION_ARCHIVE_CACHE=C:\actionarchivecache`,
		`ACTIONS_STEP_DEBUG=true`,
		`System_AccessToken=azure-secret`,
		`NUGET_API_KEY_PASSWORD=nuget-secret`,
		`builder_report_dir=D:\spoofed`,
	}
	env := TestEnvironment(base, `D:\a\builder\builder\source`, "", `D:\a\_temp\private-output\report`)
	joined := strings.Join(env, "\n")
	for _, removed := range []string{"github_output", "Github_Step_Summary", "Actions_Runtime_Token", "ACTIONS_RUNNER", "ACTIONS_STEP_DEBUG", "System_AccessToken", "NUGET_API_KEY_PASSWORD", "secret", "spoofed", "BUILDER_IOS_PATH"} {
		if strings.Contains(joined, removed) {
			t.Errorf("Windows test environment kept %q:\n%s", removed, joined)
		}
	}
	for _, kept := range []string{
		`=C:=C:\work`, `Path=C:\Windows\System32;C:\Program Files\Git\bin`, `ProgramFiles=C:\Program Files`,
		`RUNNER_TEMP=D:\a\_temp`, "RUNNER_TRACKING_ID=github_", `GITHUB_WORKSPACE=D:\a\builder\builder`,
		`BUILDER_SOURCE_DIR=D:\a\builder\builder\source`, `BUILDER_REPORT_DIR=D:\a\_temp\private-output\report`,
	} {
		if !strings.Contains(joined, kept) {
			t.Errorf("Windows test environment is missing %q:\n%s", kept, joined)
		}
	}
}

// TestTestEnvironmentScrubsThisRunner applies the scrubbing to the real
// environment of whatever machine runs the tests. On GitHub's hosted runners
// (CI runs on Linux, macOS and Windows) it logs which variable names are kept
// and removed, never their values.
func TestTestEnvironmentScrubsThisRunner(t *testing.T) {
	env := TestEnvironment(os.Environ(), "/source", "", "/report")
	kept := map[string]bool{}
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		kept[strings.ToUpper(name)] = true
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ACTIONS_") || strings.HasPrefix(upper, "INPUT_") || testEnvironmentDeniedNames[upper] ||
			strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") {
			t.Errorf("the test environment kept %s", name)
		}
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		var removed []string
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			if name != "" && !kept[strings.ToUpper(name)] {
				removed = append(removed, name)
			}
		}
		t.Logf("%s runner: removed %d variables: %s", runtime.GOOS, len(removed), strings.Join(removed, " "))
	}
}

func requirePwsh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Skip("pwsh is not installed")
	}
}

// newPwshCheckout is newTestCheckout with scripts/test.ps1.
func newPwshCheckout(t *testing.T, script string) (*testCheckout, *TestOptions) {
	t.Helper()
	checkout := newTestCheckout(t, "")
	writeTestFile(t, filepath.Join(checkout.source, "scripts", "test.ps1"), script)
	options := checkout.options(time.Minute)
	options.Script = "scripts/test.ps1"
	options.IOSPath = ""
	return checkout, options
}

func psQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func TestTestScriptCommandDispatchesByExtension(t *testing.T) {
	ps, display, err := testScriptCommand(context.Background(), "scripts/Windows-Test.PS1")
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join("scripts", "Windows-Test.PS1")}
	if base := strings.TrimSuffix(strings.ToLower(filepath.Base(ps.Path)), ".exe"); base != "pwsh" || strings.Join(ps.Args[1:], "\x00") != strings.Join(wantArgs, "\x00") {
		t.Errorf(".ps1 runs %q %q, want pwsh %q", ps.Path, ps.Args[1:], wantArgs)
	}
	if display != "pwsh "+strings.Join(wantArgs, " ") {
		t.Errorf(".ps1 log line = %q", display)
	}
	if _, err := bashExecutable(); err != nil {
		t.Skip("bash is not installed")
	}
	for _, script := range []string{"scripts/test.sh", "scripts/test", "scripts/test.bash"} {
		sh, display, err := testScriptCommand(context.Background(), script)
		if err != nil {
			t.Fatal(err)
		}
		if base := strings.TrimSuffix(strings.ToLower(filepath.Base(sh.Path)), ".exe"); base != "bash" ||
			strings.Join(sh.Args[1:], "\x00") != "--\x00"+filepath.FromSlash(script) || display != "bash -- "+script {
			t.Errorf("%s runs %q %q (%q), want bash -- %s", script, sh.Path, sh.Args[1:], display, script)
		}
	}
}

func TestRunTestsRunsPowerShellScripts(t *testing.T) {
	requirePwsh(t)
	t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(t.TempDir(), "summary"))
	t.Setenv("ACTIONS_RUNTIME_TOKEN", "runtime-secret")
	for _, exitCode := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("exit %d", exitCode), func(t *testing.T) {
			checkout, options := newPwshCheckout(t, fmt.Sprintf(`Write-Output "stdout-marker"
[Console]::Error.WriteLine("stderr-marker")
Write-Output ("cwd=" + (Get-Location).ProviderPath)
Write-Output ("source=" + $env:BUILDER_SOURCE_DIR)
Write-Output ("ios=" + ($null -eq $env:BUILDER_IOS_PATH))
if ((Get-ChildItem -Force -LiteralPath $env:BUILDER_REPORT_DIR | Measure-Object).Count -eq 0) { Write-Output "report-dir-empty" }
Write-Output ("summary=" + ($null -eq $env:GITHUB_STEP_SUMMARY) + " runtime=" + ($null -eq $env:ACTIONS_RUNTIME_TOKEN))
exit %d
`, exitCode))
			outcome, err := RunTests(context.Background(), options)
			if err != nil {
				t.Fatalf("RunTests() error = %v", err)
			}
			if outcome.ExitCode != exitCode || outcome.TimedOut || outcome.Passed() != (exitCode == 0) {
				t.Fatalf("outcome = %+v, want exit %d", outcome, exitCode)
			}
			log := readTestFile(t, options.LogPath)
			for _, want := range []string{
				"$ pwsh -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File " + filepath.Join("scripts", "test.ps1"),
				"stdout-marker", "stderr-marker",
				"cwd=" + checkout.source, "source=" + checkout.source, "ios=True", "report-dir-empty",
				"summary=True runtime=True",
				fmt.Sprintf("The test script exited with status %d.", exitCode),
			} {
				if !strings.Contains(log, want) {
					t.Errorf("private log missing %q:\n%s", want, log)
				}
			}
		})
	}
}

// startGrandchild is PowerShell that starts a detached grandchild, which
// inherits the script's stdout (the private log), sleeps, and then writes
// marker: the shape of an MSBuild node or compiler server left running.
func startGrandchild(marker string) string {
	return fmt.Sprintf(`$child = Start-Process -PassThru -NoNewWindow -FilePath (Get-Process -Id $PID).Path -ArgumentList @('-NoLogo', '-NoProfile', '-NonInteractive', '-Command', "Start-Sleep -Seconds 3; Write-Output grandchild-still-running; Set-Content -LiteralPath %s -Value late")
Write-Output ("grandchild=" + $child.Id)
`, strings.ReplaceAll(psQuote(marker), `"`, "`\""))
}

func TestRunTestsStopsTheWholeProcessTreeAfterExit(t *testing.T) {
	requirePwsh(t)
	checkout, options := newPwshCheckout(t, "")
	marker := filepath.Join(checkout.root, "late-marker")
	writeTestFile(t, filepath.Join(checkout.source, "scripts", "test.ps1"), startGrandchild(marker)+"exit 0\n")
	outcome, err := RunTests(context.Background(), options)
	if err != nil || !outcome.Passed() {
		t.Fatalf("RunTests() = %+v, %v\n%s", outcome, err, readTestFile(t, options.LogPath))
	}
	if log := readTestFile(t, options.LogPath); !strings.Contains(log, "grandchild=") {
		t.Fatalf("the grandchild did not start:\n%s", log)
	}
	// Windows refuses to delete a file another process holds open, so this
	// fails if the grandchild still has the log as its stdout.
	if err := os.Remove(options.LogPath); err != nil {
		t.Fatalf("the private log is still held open after the script exited: %v", err)
	}
	time.Sleep(5 * time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a grandchild process outlived the script")
	}
}

func TestRunTestsTimeoutStopsTheWholeProcessTree(t *testing.T) {
	requirePwsh(t)
	checkout, options := newPwshCheckout(t, "")
	marker := filepath.Join(checkout.root, "late-marker")
	writeTestFile(t, filepath.Join(checkout.source, "scripts", "test.ps1"), startGrandchild(marker)+"Start-Sleep -Seconds 120\n")
	options.Timeout = 2 * time.Second
	started := time.Now()
	outcome, err := RunTests(context.Background(), options)
	elapsed := time.Since(started)
	if err != nil || !outcome.TimedOut || outcome.Passed() {
		t.Fatalf("RunTests() = %+v, %v; want a timeout", outcome, err)
	}
	if elapsed > 45*time.Second {
		t.Fatalf("RunTests() took %s after a 2s timeout", elapsed)
	}
	if log := readTestFile(t, options.LogPath); !strings.Contains(log, "timed out after 2s") {
		t.Fatalf("private log does not record the timeout:\n%s", log)
	}
	if err := os.Remove(options.LogPath); err != nil {
		t.Fatalf("the private log is still held open after the timeout: %v", err)
	}
	time.Sleep(5 * time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a grandchild process outlived the timed-out script")
	}
}

const artifactContents = "MZ-fake-installer-bytes"

// artifactScript writes dist/EntrixSetup.exe unless skip, then exits.
func artifactScript(exitCode int, write bool) string {
	script := "New-Item -ItemType Directory -Force -Path dist | Out-Null\n"
	if write {
		script += "[IO.File]::WriteAllText((Join-Path $env:BUILDER_SOURCE_DIR 'dist/EntrixSetup.exe'), '" + artifactContents + "')\n"
	}
	script += "Set-Content -LiteralPath (Join-Path $env:BUILDER_REPORT_DIR 'report.md') -Value '# Windows tests'\n"
	return script + fmt.Sprintf("Write-Output 'private-windows-output'\nexit %d\n", exitCode)
}

func TestExecuteTestsSecureEncryptsArtifactOnlyAfterAPass(t *testing.T) {
	requirePwsh(t)
	for _, test := range []struct {
		name        string
		script      string
		wantErr     error
		wantMembers string
		wantNote    string
	}{
		{"pass with artifact", artifactScript(0, true), nil, "artifact.age,report.md.age,test.log.age", "The artifact dist/EntrixSetup.exe (23 bytes) was encrypted."},
		{"failure with artifact", artifactScript(1, true), ErrTestsFailed, "report.md.age,test.log.age", "The tests did not pass, so the artifact dist/EntrixSetup.exe was not encrypted or uploaded."},
		{"pass without artifact", artifactScript(0, false), ErrTestsFailed, "report.md.age,test.log.age", "is unusable (artifact does not exist in the snapshot), so the run failed."},
		{"pass with a directory as artifact", "New-Item -ItemType Directory -Force -Path dist/EntrixSetup.exe | Out-Null\nexit 0\n", ErrTestsFailed, "test.log.age", "artifact is not a regular file"},
		{"pass with an empty artifact", "New-Item -ItemType Directory -Force -Path dist | Out-Null\nNew-Item -ItemType File -Path dist/EntrixSetup.exe | Out-Null\nexit 0\n", ErrTestsFailed, "test.log.age", "artifact is empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkout, options := newPwshCheckout(t, test.script)
			options.Artifact = "dist/EntrixSetup.exe"
			err := ExecuteTestsSecure(context.Background(), options, checkout.identity.Recipient().String(), checkout.encrypted)
			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) {
				t.Fatalf("ExecuteTestsSecure() error = %v, want %v", err, test.wantErr)
			}
			if got := strings.Join(dirNames(t, checkout.encrypted), ","); got != test.wantMembers {
				t.Fatalf("encrypted members = %v, want %v", got, test.wantMembers)
			}
			if leftovers := dirNames(t, checkout.privateDir); len(leftovers) != 0 {
				t.Fatalf("plaintext left in private output: %v", leftovers)
			}
			log := decryptFile(t, filepath.Join(checkout.encrypted, "test.log.age"), checkout.identity)
			if !strings.Contains(log, test.wantNote) {
				t.Fatalf("decrypted log does not contain %q:\n%s", test.wantNote, log)
			}
			if test.wantMembers != "test.log.age" && !strings.Contains(log, "private-windows-output") {
				t.Fatalf("decrypted log does not contain the script's output:\n%s", log)
			}
			if strings.HasPrefix(test.wantMembers, "artifact.age") {
				if got := decryptFile(t, filepath.Join(checkout.encrypted, "artifact.age"), checkout.identity); got != artifactContents {
					t.Fatalf("decrypted artifact = %q", got)
				}
			}
		})
	}
}

func TestExecuteTestsSecureRejectsHostileArtifactOptions(t *testing.T) {
	checkout := newTestCheckout(t, "exit 0\n")
	recipient := checkout.identity.Recipient().String()
	for _, artifact := range []string{"../outside.exe", ".git/config", "/etc/passwd", `dist\x.exe`, "dist/x.exe."} {
		options := checkout.options(time.Minute)
		options.Artifact = artifact
		if err := ExecuteTestsSecure(context.Background(), options, recipient, checkout.encrypted); err == nil || errors.Is(err, ErrTestsFailed) {
			t.Errorf("artifact %q: ExecuteTestsSecure() error = %v, want a validation error", artifact, err)
		}
	}
}

func TestResolveTestArtifactRejectsSymlinkEscapes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	checkout := newTestCheckout(t, "exit 0\n")
	outside := filepath.Join(checkout.root, "outside.exe")
	writeTestFile(t, outside, "private")
	dist := filepath.Join(checkout.source, "dist")
	writeTestFile(t, filepath.Join(dist, "real.exe"), "MZ")
	for name, target := range map[string]string{"escape.exe": outside, "git.exe": "../.git/config", "alias.exe": "real.exe"} {
		if err := os.Symlink(target, filepath.Join(dist, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, artifact := range []string{"dist/escape.exe", "dist/git.exe", "dist/missing.exe"} {
		if _, err := ResolveTestArtifact(checkout.source, artifact); err == nil {
			t.Errorf("ResolveTestArtifact(%q) accepted a target outside the checkout's regular files", artifact)
		}
	}
	if resolved, err := ResolveTestArtifact(checkout.source, "dist/alias.exe"); err != nil || resolved != filepath.Join(dist, "real.exe") {
		t.Errorf("ResolveTestArtifact(alias) = %q, %v", resolved, err)
	}
}

func TestEncryptSnapshotArtifactEnforcesTheSizeCap(t *testing.T) {
	checkout := newTestCheckout(t, "")
	recipient := checkout.identity.Recipient()
	if err := os.MkdirAll(checkout.encrypted, 0700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(checkout.encrypted, "artifact.age")
	writeTestFile(t, filepath.Join(checkout.source, "dist", "at-limit.exe"), strings.Repeat("x", 1000))
	if size, err := encryptSnapshotArtifact(recipient, checkout.source, "dist/at-limit.exe", destination, 1000); err != nil || size != 1000 {
		t.Fatalf("an artifact at the limit: size %d, %v", size, err)
	}
	if got := decryptFile(t, destination, checkout.identity); got != strings.Repeat("x", 1000) {
		t.Fatalf("decrypted artifact has %d bytes", len(got))
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(checkout.source, "dist", "over.exe"), strings.Repeat("x", 1001))
	if _, err := encryptSnapshotArtifact(recipient, checkout.source, "dist/over.exe", destination, 1000); err == nil || !strings.Contains(err.Error(), "over the 1000-byte limit") {
		t.Fatalf("an artifact over the limit: %v", err)
	}
	if names := dirNames(t, checkout.encrypted); len(names) != 0 {
		t.Fatalf("a rejected artifact left %v behind", names)
	}
	if MaxTestArtifactBytes != 1<<30 {
		t.Fatalf("MaxTestArtifactBytes = %d, want 1 GiB", MaxTestArtifactBytes)
	}
}

func TestEncryptSnapshotArtifactStreams(t *testing.T) {
	checkout := newTestCheckout(t, "")
	const size = 64 << 20
	path := filepath.Join(checkout.source, "dist", "big.exe")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	for written := 0; written < size; written += len(chunk) {
		if _, err := file.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(checkout.encrypted, 0700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(checkout.encrypted, "artifact.age")
	var written int64
	peak := peakHeapDuring(func() {
		written, err = encryptSnapshotArtifact(checkout.identity.Recipient(), checkout.source, "dist/big.exe", destination, MaxTestArtifactBytes)
	})
	if err != nil || written != size {
		t.Fatalf("encryptSnapshotArtifact() = %d, %v", written, err)
	}
	if peak > 24<<20 {
		t.Fatalf("encrypting a %d MiB artifact held %d MiB of heap; it must stream", size>>20, peak>>20)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Size() <= size {
		t.Fatalf("ciphertext = %v, %v", info, err)
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
