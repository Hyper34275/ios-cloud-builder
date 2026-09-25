package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/spf13/cobra"
)

var errTestsFailed = errors.New("tests failed")

var iosTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Run the project's test script on the central builder",
	Long: `Snapshots the working tree and runs the project's test script on a macOS
runner of the central builder, with Xcode 26 and iOS 26 simulators.

The script (--script, or ios.testScript in builder.json) is a path relative to
the repository root and runs as "bash <script>" from the repository root. It
receives BUILDER_SOURCE_DIR, BUILDER_IOS_PATH and BUILDER_REPORT_DIR, and may
write a Markdown summary to $BUILDER_REPORT_DIR/report.md. Its exit status
decides whether the tests passed.

Everything the script prints, and its report, are encrypted to your local AGE
identity before they leave the runner; the public run shows only whether the
tests passed. The decrypted log and report are written to the output directory
and the report is printed here. The command exits non-zero when the tests fail.`,
	Args: cobra.NoArgs,
	RunE: runIOSTest,
}

func init() {
	iosTestCmd.Flags().String("script", "", "Test script relative to the repository root (default: ios.testScript in builder.json)")
	iosTestCmd.Flags().Duration("timeout", build.DefaultTestTimeout, "How long to wait for the run, including queueing")
	iosTestCmd.Flags().StringP("output", "o", "dist", "Output directory for the decrypted log and report")
	iosTestCmd.Flags().StringP("remote", "r", "origin", "Git remote to push the working-tree snapshot to")
	iosCmd.AddCommand(iosTestCmd)
}

func runIOSTest(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if !cfg.IsCentral() {
		return fmt.Errorf("`builder ios test` requires backend=central")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	repoRoot, err := gitTopLevel(ctx)
	if err != nil {
		return err
	}
	flagScript, _ := cmd.Flags().GetString("script")
	script, err := resolveTestScript(flagScript, cfg.IOS.TestScript, repoRoot)
	if err != nil {
		return err
	}
	ignored, err := testScriptIgnored(ctx, repoRoot, script)
	if err != nil {
		return err
	}
	if ignored {
		return fmt.Errorf("test script %s is excluded by .gitignore, so the snapshot would not contain it", script)
	}
	outputDir, _ := cmd.Flags().GetString("output")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	remote, _ := cmd.Flags().GetString("remote")

	ghClient, err := getGitHubClient()
	if err != nil {
		return err
	}
	result, err := build.NewCoordinator(cfg, ghClient).Test(ctx, build.TestOptions{
		OutputDir: outputDir,
		Timeout:   timeout,
		Remote:    remote,
		Script:    script,
	})
	if err != nil {
		return err
	}
	printTestResult(os.Stdout, result)
	if !result.Passed {
		return errTestsFailed
	}
	return nil
}

// resolveTestScript picks the test script, --script over ios.testScript, and
// proves locally that it names a regular file inside the repository, so a
// typo fails before anything is snapshotted or dispatched. It returns the
// forward-slash path the workflow receives.
func resolveTestScript(flagValue, configValue, repoRoot string) (string, error) {
	value, source := strings.TrimSpace(flagValue), "--script"
	if value == "" {
		value, source = configValue, "ios.testScript in builder.json"
	} else {
		if filepath.IsAbs(value) {
			relative, err := filepath.Rel(repoRoot, value)
			if err != nil {
				return "", fmt.Errorf("--script %s is outside the repository %s", value, repoRoot)
			}
			value = relative
		}
		value = filepath.ToSlash(filepath.Clean(value))
	}
	if value == "" {
		return "", errors.New("no test script: pass --script <path> or set ios.testScript in builder.json to a path relative to the repository root, such as scripts/ios-test.sh")
	}
	if err := config.ValidateTestScriptPath(value); err != nil {
		return "", fmt.Errorf("test script %q from %s %v", value, source, err)
	}
	local := filepath.Join(repoRoot, filepath.FromSlash(value))
	info, err := os.Stat(local)
	switch {
	case os.IsNotExist(err):
		return "", fmt.Errorf("test script %s from %s does not exist in %s", value, source, repoRoot)
	case err != nil:
		return "", fmt.Errorf("inspect test script %s: %w", value, err)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("test script %s from %s is not a regular file", value, source)
	}
	root, rootErr := filepath.EvalSymlinks(repoRoot)
	resolved, resolveErr := filepath.EvalSymlinks(local)
	if rootErr != nil || resolveErr != nil || !pathInside(root, resolved) {
		return "", fmt.Errorf("test script %s resolves outside the repository", value)
	}
	return value, nil
}

func pathInside(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// gitTopLevel is the repository root: the snapshot's root on the runner, and
// the base that ios.path and the test script are relative to.
func gitTopLevel(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find the repository root (is this a Git working tree?): %w", err)
	}
	return filepath.FromSlash(strings.TrimSpace(string(output))), nil
}

// testScriptIgnored reports whether .gitignore excludes an untracked script,
// which would leave it out of the snapshot. Tracked files are always included.
func testScriptIgnored(ctx context.Context, repoRoot, script string) (bool, error) {
	err := exec.CommandContext(ctx, "git", "-C", repoRoot, "check-ignore", "-q", "--", script).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether the test script is ignored: %w", err)
}

func printTestResult(w io.Writer, result *build.TestResult) {
	fmt.Fprintln(w)
	if len(result.Report) > 0 {
		report := terminalSafe(result.Report)
		fmt.Fprintln(w, "──── report.md ────")
		fmt.Fprint(w, report)
		if !strings.HasSuffix(report, "\n") {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "───────────────────")
	} else {
		fmt.Fprintln(w, "The test script wrote no $BUILDER_REPORT_DIR/report.md; see the log.")
	}
	if result.Passed {
		fmt.Fprintln(w, "Tests passed")
	} else {
		fmt.Fprintf(w, "Tests failed (workflow concluded %s)\n", result.Conclusion)
	}
	if result.ReportPath != "" {
		fmt.Fprintf(w, "Report: %s\n", result.ReportPath)
	}
	fmt.Fprintf(w, "Log: %s\n", result.LogPath)
	fmt.Fprintf(w, "Workflow: %s\n", result.WorkflowURL)
}

// terminalSafe drops control characters other than newline and tab, so a
// decrypted report cannot move the cursor, retitle the terminal, or hide text.
func terminalSafe(data []byte) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(string(data), "�"))
}
