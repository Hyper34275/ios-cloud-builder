package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxMacOSBuildTimeout bounds the project script of a macOS deployment build;
// the build job itself has 45 minutes.
const MaxMacOSBuildTimeout = 40 * time.Minute

// MacOSBuildOptions are the inputs of the build half of a macOS App Store
// deployment. There is no fixed macOS build pipeline to run: the project's
// own script (test_script) builds the application, and the bundle it leaves
// at App (artifact_path) is packaged exactly like an unsigned iOS application,
// as Payload/<name>.app in App.ipa, so that encryption, upload and the
// protected job's extraction stay shared with iOS.
type MacOSBuildOptions struct {
	SourceRoot string        // absolute private checkout; the script's working directory
	Script     string        // test_script input, relative to SourceRoot
	App        string        // artifact_path input: the .app the script builds, relative to SourceRoot
	LogPath    string        // <private-output>/build.log
	IPAPath    string        // <private-output>/App.ipa
	Timeout    time.Duration // limit for the script
}

func (options *MacOSBuildOptions) validate() error {
	if options == nil {
		return fmt.Errorf("missing macOS build options")
	}
	if err := ValidateTestScriptPath(options.Script); err != nil {
		return fmt.Errorf("invalid macOS build script path")
	}
	if err := ValidateMacOSAppPath(options.App); err != nil {
		return fmt.Errorf("invalid macOS application path")
	}
	if options.Timeout <= 0 || options.Timeout > MaxMacOSBuildTimeout {
		return fmt.Errorf("invalid macOS build timeout")
	}
	for _, path := range []string{options.SourceRoot, options.LogPath, options.IPAPath} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("runner paths must be absolute")
		}
	}
	if filepath.Base(options.LogPath) != "build.log" || filepath.Base(options.IPAPath) != "App.ipa" ||
		filepath.Dir(options.LogPath) != filepath.Dir(options.IPAPath) ||
		filepath.Base(filepath.Dir(options.LogPath)) != "private-output" ||
		pathWithin(options.SourceRoot, filepath.Dir(options.LogPath)) {
		return fmt.Errorf("invalid private output paths")
	}
	return nil
}

// ExecuteMacOSBuildSecure runs the project's macOS build script and packages
// the application it built, then encrypts the private log for the caller and
// the unsigned package for ipaRecipient (the protected signing job), exactly
// like ExecuteSecureWithIPARecipient does for an iOS build.
func ExecuteMacOSBuildSecure(ctx context.Context, options *MacOSBuildOptions, logRecipient, ipaRecipient, encryptedDir string) error {
	if err := options.validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(encryptedDir) || filepath.Base(encryptedDir) != "encrypted" ||
		filepath.Dir(encryptedDir) != filepath.Dir(filepath.Dir(options.LogPath)) {
		return fmt.Errorf("invalid encrypted output directory")
	}
	buildErr := buildMacOSUnsigned(ctx, options)
	if _, err := os.Lstat(options.LogPath); os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(options.LogPath), 0700)
		message := []byte("The macOS build could not start. No public diagnostic details were emitted.\n")
		_ = os.WriteFile(options.LogPath, message, 0600)
	}
	if buildErr != nil {
		// A failed build uploads its log only, never a half-built package.
		_ = os.Remove(options.IPAPath)
	}
	if err := EncryptArtifactsWithRecipients(logRecipient, ipaRecipient, options.LogPath, options.IPAPath, encryptedDir); err != nil {
		return fmt.Errorf("encrypt private build artifacts")
	}
	if buildErr != nil {
		return ErrBuildFailed
	}
	return nil
}

func buildMacOSUnsigned(ctx context.Context, options *MacOSBuildOptions) error {
	if err := os.MkdirAll(filepath.Dir(options.LogPath), 0700); err != nil {
		return fmt.Errorf("prepare private output")
	}
	logFile, err := os.OpenFile(options.LogPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("prepare private build log")
	}
	defer logFile.Close()
	if err := buildMacOSApp(ctx, options, logFile); err != nil {
		fmt.Fprintf(logFile, "\nBuild failed: %v\n", err)
		return ErrBuildFailed
	}
	if err := logFile.Sync(); err != nil {
		return ErrBuildFailed
	}
	return nil
}

func buildMacOSApp(ctx context.Context, options *MacOSBuildOptions, privateLog *os.File) error {
	sourceRoot, err := filepath.EvalSymlinks(options.SourceRoot)
	if err != nil {
		return fmt.Errorf("resolve source checkout: %w", err)
	}
	if err := VerifyCheckoutNoCredentials(sourceRoot); err != nil {
		return err
	}
	if _, err := ResolveTestScript(sourceRoot, options.Script); err != nil {
		return err
	}
	privateHome := filepath.Join(filepath.Dir(options.LogPath), ".build-home")
	_ = os.RemoveAll(privateHome)
	if err := os.Mkdir(privateHome, 0700); err != nil {
		return fmt.Errorf("create isolated build home: %w", err)
	}
	defer func() { _ = os.RemoveAll(privateHome) }()

	scriptCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	cmd, display, err := testScriptCommand(scriptCtx, options.Script)
	if err != nil {
		return err
	}
	cmd.Dir = sourceRoot
	// The same allowlisted environment and isolated HOME as an iOS build, so
	// no Actions runtime or credential value reaches the project's script.
	cmd.Env = append(ChildEnvironment(sourceRoot, privateHome), "BUILDER_SOURCE_DIR="+sourceRoot)
	cmd.Stdout = privateLog
	cmd.Stderr = privateLog
	cmd.WaitDelay = testTerminateGrace
	tree, err := newProcessTree(cmd)
	if err != nil {
		return fmt.Errorf("isolate build script: %w", err)
	}
	defer tree.stop()
	fmt.Fprintf(privateLog, "$ %s\n(working directory %s, timeout %s)\n\n", display, sourceRoot, options.Timeout)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start build script: %w", err)
	}
	if err := tree.started(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("isolate build script: %w", err)
	}
	waitErr := cmd.Wait()
	tree.stop()
	switch {
	case errors.Is(scriptCtx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("build script timed out after %s", options.Timeout)
	case waitErr != nil:
		return fmt.Errorf("build script failed: %w", waitErr)
	}

	appPath, err := ResolveMacOSApp(sourceRoot, options.App)
	if err != nil {
		return err
	}
	run := executor{ctx: ctx, env: ChildEnvironment(sourceRoot, privateHome), log: privateLog}
	return packageIPA(run, appPath, options.IPAPath)
}

// ResolveMacOSApp proves that app, once every symlink is resolved, names an
// application bundle directory inside sourceRoot and outside its Git
// metadata, with a Contents/Info.plist, and that the bundle holds nothing but
// directories and regular files. The protected job refuses symbolic links in
// the package it extracts, so a bundle with one (such as a versioned
// framework) fails here with a clear message rather than after upload.
func ResolveMacOSApp(sourceRoot, app string) (string, error) {
	if err := ValidateMacOSAppPath(app); err != nil {
		return "", fmt.Errorf("invalid macOS application path")
	}
	root, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve source checkout")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(app)))
	if err != nil {
		return "", fmt.Errorf("the build script did not produce %s", app)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || !pathWithin(root, resolved) {
		return "", fmt.Errorf("macOS application resolves outside the snapshot")
	}
	if first, _, _ := strings.Cut(filepath.ToSlash(relative), "/"); strings.EqualFold(first, ".git") {
		return "", fmt.Errorf("macOS application resolves into Git metadata")
	}
	if info, err := os.Lstat(resolved); err != nil || !info.IsDir() {
		return "", fmt.Errorf("macOS application is not a bundle directory")
	}
	if info, err := os.Lstat(filepath.Join(resolved, "Contents", "Info.plist")); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("macOS application has no Contents/Info.plist")
	}
	err = filepath.WalkDir(resolved, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("macOS application contains a symbolic link, which is not supported")
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("macOS application contains a special file")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return resolved, nil
}
