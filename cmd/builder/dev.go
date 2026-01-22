package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/dev"
	"github.com/MobAI-App/ios-builder/internal/mobai"
	"github.com/spf13/cobra"
)

var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "Development commands",
}

var devFlutterCmd = &cobra.Command{
	Use:   "flutter",
	Short: "Start Flutter hot reload session",
	Long: `Starts a Flutter hot reload session using MobAI.

Prerequisites:
- MobAI running with an iOS device connected
- IPA built with 'builder ios build' (must be a debug build)
- Flutter SDK installed`,
	RunE: runDevFlutter,
}

func init() {
	devCmd.AddCommand(devFlutterCmd)
	devFlutterCmd.Flags().StringP("device", "d", "", "Device ID (default: first available)")
	devFlutterCmd.Flags().String("mobai-url", mobai.DefaultBaseURL, "MobAI API URL")
	devFlutterCmd.Flags().String("ipa", "", "Path to IPA (default: auto-detect from dist/)")
	devFlutterCmd.Flags().Bool("skip-install", false, "Skip app installation (app must already be installed)")
	devFlutterCmd.Flags().String("bundle-id", "", "Bundle ID (required with --skip-install)")
}

func runDevFlutter(cmd *cobra.Command, args []string) error {
	deviceID, _ := cmd.Flags().GetString("device")
	mobaiURL, _ := cmd.Flags().GetString("mobai-url")
	ipaPath, _ := cmd.Flags().GetString("ipa")
	skipInstall, _ := cmd.Flags().GetBool("skip-install")
	bundleID, _ := cmd.Flags().GetString("bundle-id")

	if cfg, err := config.NewManager().Load(); err == nil {
		if !cmd.Flags().Changed("mobai-url") && cfg.MobAI.URL != "" {
			mobaiURL = cfg.MobAI.URL
		}
		if !cmd.Flags().Changed("device") && cfg.MobAI.DeviceID != "" {
			deviceID = cfg.MobAI.DeviceID
		}
	}

	if skipInstall {
		if bundleID == "" {
			return fmt.Errorf("--bundle-id is required when using --skip-install")
		}
	} else {
		if ipaPath == "" {
			var err error
			ipaPath, err = dev.FindIPA("dist")
			if err != nil {
				cmd.SilenceUsage = true
				return err
			}
		}

		if _, err := os.Stat(ipaPath); os.IsNotExist(err) {
			cmd.SilenceUsage = true
			return fmt.Errorf("IPA not found: %s", ipaPath)
		}

		fmt.Printf("Found IPA: %s\n", ipaPath)
		fmt.Println()
	}

	session := dev.NewSession(mobaiURL, deviceID, ipaPath)
	session.SetSkipInstall(skipInstall, bundleID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		cancel()
		session.Stop()
	}()

	err := session.Start(ctx)
	session.Stop()

	if err != nil {
		cmd.SilenceUsage = true
		// Check if it's a runtime error and give helpful message
		if _, ok := err.(*dev.RuntimeError); ok {
			fmt.Println()
			fmt.Println("Tip: If connection failed, try:")
			fmt.Println("  1. Reconnect the device (unplug/replug)")
			fmt.Println("  2. Restart MobAI")
			fmt.Println("  3. Run 'builder mobai ping' to verify connection")
		}
	}

	return err
}
