package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/spf13/cobra"
)

var macosCmd = &cobra.Command{
	Use:   "macos",
	Short: "macOS deployment commands",
}

var macosDeployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Build, sign, and upload a macOS application to App Store Connect",
	Long: `Builds the private working-tree snapshot on the central builder by running
macos.buildScript from builder.json, which must leave the application at
macos.app, signed ad hoc with its App Sandbox entitlements. The protected
apple-production Environment then signs it with the Apple Distribution identity
under a Mac App Store profile, packages it as an installer signed with the Mac
Installer Distribution identity, and uploads it to App Store Connect.

When apple-production has no MAC_INSTALLER_P12 yet, the deployment creates a
Mac Installer Distribution certificate through the App Store Connect API and
saves it, decrypted, to the output directory. Store its two fields as the
MAC_INSTALLER_P12 and MAC_INSTALLER_P12_PASSWORD secrets of apple-production
and delete the file, so later deployments reuse it.`,
	RunE: runMacOSDeploy,
}

func init() {
	macosDeployCmd.Flags().StringP("output", "o", "dist", "Output directory for decrypted diagnostic logs")
	macosDeployCmd.Flags().Duration("timeout", 45*time.Minute, "Deployment timeout, including Environment approval")
	macosDeployCmd.Flags().StringP("remote", "r", "origin", "Git remote to push the working-tree snapshot to")
	macosCmd.AddCommand(macosDeployCmd)
	rootCmd.AddCommand(macosCmd)
}

func runMacOSDeploy(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if !cfg.IsCentral() {
		return fmt.Errorf("`builder macos deploy` requires backend=central")
	}
	if cfg.MacOS.BuildScript == "" || cfg.MacOS.App == "" {
		return fmt.Errorf("builder.json needs macos.buildScript and macos.app")
	}
	outputDir, _ := cmd.Flags().GetString("output")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	remote, _ := cmd.Flags().GetString("remote")
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	ghClient, err := getGitHubClient()
	if err != nil {
		return err
	}
	result, err := build.NewCoordinator(cfg, ghClient).Build(ctx, build.BuildOptions{
		OutputDir:  outputDir,
		Timeout:    timeout,
		TestFlight: true,
		MacOS:      true,
		Remote:     remote,
	})
	if err != nil {
		return err
	}
	fmt.Println("App Store Connect: macOS upload accepted")
	if result.InstallerIdentityPath != "" {
		fmt.Printf("New Mac installer identity: %s\n", result.InstallerIdentityPath)
		fmt.Println("Store its p12 and password as MAC_INSTALLER_P12 and MAC_INSTALLER_P12_PASSWORD in apple-production, then delete the file.")
	}
	fmt.Printf("Workflow: %s\n", result.WorkflowURL)
	return nil
}
