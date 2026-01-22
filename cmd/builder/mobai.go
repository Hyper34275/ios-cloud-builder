package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/MobAI-App/ios-builder/internal/mobai"
	"github.com/spf13/cobra"
)

var mobaiCmd = &cobra.Command{
	Use:   "mobai",
	Short: "MobAI device commands for Flutter custom-devices",
}

var mobaiPingCmd = &cobra.Command{
	Use:   "ping",
	Short: "Check MobAI connectivity",
	RunE:  runMobaiPing,
}

var mobaiInstallCmd = &cobra.Command{
	Use:   "install <ipa-path>",
	Short: "Install app via MobAI",
	Args:  cobra.ExactArgs(1),
	RunE:  runMobaiInstall,
}

var mobaiRunDebugCmd = &cobra.Command{
	Use:   "run-debug <bundle-id>",
	Short: "Run app with debugger",
	Args:  cobra.ExactArgs(1),
	RunE:  runMobaiRunDebug,
}

var mobaiForwardCmd = &cobra.Command{
	Use:   "forward <device-port> <host-port>",
	Short: "Forward port via MobAI",
	Args:  cobra.ExactArgs(2),
	RunE:  runMobaiForward,
}

func init() {
	mobaiCmd.AddCommand(mobaiPingCmd)
	mobaiCmd.AddCommand(mobaiInstallCmd)
	mobaiCmd.AddCommand(mobaiRunDebugCmd)
	mobaiCmd.AddCommand(mobaiForwardCmd)

	mobaiCmd.PersistentFlags().String("url", mobai.DefaultBaseURL, "MobAI API URL")
	mobaiCmd.PersistentFlags().StringP("device", "d", "", "Device ID")
}

func getMobaiClient(cmd *cobra.Command) *mobai.Client {
	url, _ := cmd.Flags().GetString("url")
	return mobai.NewClient(url)
}

func getDeviceID(cmd *cobra.Command, client *mobai.Client) (string, error) {
	deviceID, _ := cmd.Flags().GetString("device")
	if deviceID != "" {
		return deviceID, nil
	}

	// Check environment variable (set by builder dev flutter)
	if envID := os.Getenv("MOBAI_DEVICE_ID"); envID != "" {
		return envID, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	devices, err := client.ListDevices(ctx)
	if err != nil {
		return "", err
	}
	if len(devices) == 0 {
		return "", fmt.Errorf("no devices connected")
	}
	return devices[0].ID, nil
}

func runMobaiPing(cmd *cobra.Command, args []string) error {
	client := getMobaiClient(cmd)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	devices, err := client.ListDevices(ctx)
	if err != nil {
		return fmt.Errorf("cannot connect to MobAI: %w", err)
	}

	if len(devices) == 0 {
		return fmt.Errorf("no devices connected")
	}

	fmt.Printf("success\n")
	return nil
}

func runMobaiInstall(cmd *cobra.Command, args []string) error {
	client := getMobaiClient(cmd)
	deviceID, err := getDeviceID(cmd, client)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req := mobai.InstallAppRequest{Path: args[0]}
	_, err = client.InstallApp(ctx, deviceID, req)
	if err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	fmt.Println("installed")
	return nil
}

func runMobaiRunDebug(cmd *cobra.Command, args []string) error {
	client := getMobaiClient(cmd)
	deviceID, err := getDeviceID(cmd, client)
	if err != nil {
		return err
	}

	bundleID := args[0]
	ctx := context.Background()

	outputChan, conn, err := client.DebugStream(ctx, deviceID, bundleID)
	if err != nil {
		return fmt.Errorf("debug failed: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Stream output and print VM service URL when found
	for output := range outputChan {
		if output.Type == "stdout" || output.Type == "stderr" {
			fmt.Fprint(os.Stderr, output.Data)
		}
	}

	return nil
}

func runMobaiForward(cmd *cobra.Command, args []string) error {
	client := getMobaiClient(cmd)
	deviceID, err := getDeviceID(cmd, client)
	if err != nil {
		return err
	}

	devicePort, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid device port: %w", err)
	}

	hostPort, err := strconv.Atoi(args[1])
	if err != nil {
		return fmt.Errorf("invalid host port: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.ForwardPort(ctx, deviceID, mobai.PortForwardRequest{
		DevicePort: devicePort,
		HostPort:   hostPort,
	})
	if err != nil {
		return fmt.Errorf("forward failed: %w", err)
	}

	fmt.Printf("forwarded %d -> %d\n", resp.DevicePort, resp.HostPort)

	// Keep running to maintain the forward
	select {}
}
