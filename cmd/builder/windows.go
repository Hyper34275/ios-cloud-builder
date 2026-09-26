package main

import (
	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/spf13/cobra"
)

var windowsCmd = &cobra.Command{
	Use:   "windows",
	Short: "Test and build Windows applications on the central builder",
}

var windowsTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Run the project's Windows test script on the central builder and fetch its artifact",
	Long: `Snapshots the working tree and runs the project's test script on a Windows
runner (windows-2025) of the central builder.

The script (--script, or windows.testScript in builder.json) is a .ps1 or .sh
file relative to the repository root. A .ps1 runs as
"pwsh -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File <script>"
and a .sh as "bash -- <script>" (Git for Windows), from the repository root.
It receives BUILDER_SOURCE_DIR and BUILDER_REPORT_DIR, may write a Markdown
summary to $env:BUILDER_REPORT_DIR/report.md, and its exit status decides
whether the tests passed.

--artifact (or windows.artifact) names a file the script builds, such as
dist/Setup.exe. Only when the script exits 0 is that file encrypted and
returned; it is written to the output directory under its base name. A
missing or unusable artifact fails the run.

Everything leaves the runner encrypted to your local AGE identity; the public
run shows only whether the tests passed. When the tests pass and every output
is decrypted, the command deletes the run and its encrypted artifact from the
public builder (--keep-run keeps them); a run whose tests did not pass is kept,
with its encrypted artifact for one day. The command exits non-zero when the
tests fail.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error { return runCentralTest(cmd, build.PlatformWindows) },
}

func init() {
	windowsTestCmd.Flags().String("script", "", "Test script (.ps1 or .sh) relative to the repository root (default: windows.testScript in builder.json)")
	windowsTestCmd.Flags().String("artifact", "", "File the passing script builds, relative to the repository root (default: windows.artifact in builder.json; --artifact= for none)")
	windowsTestCmd.Flags().Duration("timeout", build.DefaultWindowsTestTimeout, "How long to wait for the run, including queueing")
	windowsTestCmd.Flags().StringP("output", "o", "dist", "Output directory for the decrypted log, report and artifact")
	windowsTestCmd.Flags().StringP("remote", "r", "origin", "Git remote to push the working-tree snapshot to")
	windowsTestCmd.Flags().Bool("keep-run", false, "Keep the public workflow run and its encrypted artifact after the tests pass")
	windowsCmd.AddCommand(windowsTestCmd)
	rootCmd.AddCommand(windowsCmd)
}
