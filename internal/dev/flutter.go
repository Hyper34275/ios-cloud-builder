package dev

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/MobAI-App/ios-builder/internal/mobai"
)

// FlutterHandler implements FrameworkHandler for Flutter projects.
type FlutterHandler struct {
	mobaiURL string
}

// NewFlutterHandler creates a new Flutter handler.
func NewFlutterHandler(mobaiURL string) *FlutterHandler {
	return &FlutterHandler{mobaiURL: mobaiURL}
}

// Setup configures Flutter custom device for MobAI.
func (h *FlutterHandler) Setup(ctx context.Context) error {
	return EnsureCustomDevice(h.mobaiURL)
}

// Attach finds the VM Service URL and runs flutter attach.
func (h *FlutterHandler) Attach(ctx context.Context, _ *mobai.Client, deviceID string, debugOutput <-chan mobai.DebugOutput) error {
	fmt.Println("Waiting for debug service...")

	debugURL, outputChan, err := h.findDebugURL(ctx, debugOutput)
	if err != nil {
		return err
	}

	return h.runFlutterAttach(ctx, deviceID, debugURL, outputChan)
}

// DebugConfig returns nil for Flutter (no special environment needed).
func (h *FlutterHandler) DebugConfig() *mobai.DebugConfig {
	return nil
}

func (h *FlutterHandler) Stop() {}

func (h *FlutterHandler) findDebugURL(ctx context.Context, debugOutput <-chan mobai.DebugOutput) (string, <-chan mobai.DebugOutput, error) {
	re := regexp.MustCompile(`(?:Observatory|Dart VM service)[^h]*(http://[^\s]+)`)
	timeout := time.After(30 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-timeout:
			return "", nil, fmt.Errorf("timeout waiting for debug service - is this a debug build?")
		case output, ok := <-debugOutput:
			if !ok {
				return "", nil, fmt.Errorf("debug stream closed")
			}
			if output.Type == "error" {
				return "", nil, fmt.Errorf("debug error: %s", output.Message)
			}
			if m := re.FindStringSubmatch(output.Data); len(m) >= 2 {
				fmt.Printf("Found debug service: %s\n", m[1])
				return m[1], debugOutput, nil
			}
		}
	}
}

func (h *FlutterHandler) runFlutterAttach(ctx context.Context, deviceID, debugURL string, outputChan <-chan mobai.DebugOutput) error {
	fmt.Println()
	fmt.Printf("VM Service URL (on device): %s\n", debugURL)
	fmt.Println()

	go func() {
		for output := range outputChan {
			if output.Type == "exit" {
				return
			}
		}
	}()

	fmt.Println("Attaching Flutter debugger...")
	fmt.Println()

	cmd := exec.CommandContext(ctx, "flutter", "attach", "-d", "mobai-ios", "--debug-url="+debugURL)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "MOBAI_DEVICE_ID="+deviceID)

	err := cmd.Run()
	if err != nil {
		return &RuntimeError{Err: err}
	}
	return nil
}

// EnsureCustomDevice ensures Flutter custom_devices.json has mobai-ios configured.
func EnsureCustomDevice(mobaiURL string) error {
	if err := exec.Command("flutter", "config", "--enable-custom-devices").Run(); err != nil {
		return fmt.Errorf("failed to enable custom devices - run 'flutter config --enable-custom-devices' manually first: %w", err)
	}

	var configPath string
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return fmt.Errorf("APPDATA environment variable not set")
		}
		configPath = filepath.Join(appData, ".flutter_custom_devices.json")
	} else {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home dir: %w", err)
		}
		flutterDir := filepath.Join(homeDir, ".config", "flutter")
		if err := os.MkdirAll(flutterDir, 0755); err != nil {
			return fmt.Errorf("create flutter config dir: %w", err)
		}
		configPath = filepath.Join(flutterDir, "custom_devices.json")
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	pingCmd := []string{execPath, "mobai", "--url", mobaiURL, "ping"}
	installCmd := []string{execPath, "mobai", "--url", mobaiURL, "install", "${localPath}"}
	runDebugCmd := []string{execPath, "mobai", "--url", mobaiURL, "run-debug", "${appName}"}
	forwardCmd := []string{execPath, "mobai", "--url", mobaiURL, "forward", "${devicePort}", "${hostPort}"}

	config := map[string]any{
		"custom-devices": []any{
			map[string]any{
				"id":                      "mobai-ios",
				"label":                   "MobAI iOS Device",
				"sdkNameAndVersion":       "iOS (via MobAI)",
				"platform":                nil,
				"enabled":                 true,
				"ping":                    pingCmd,
				"pingSuccessRegex":        "success",
				"install":                 installCmd,
				"uninstall":               []string{"echo", "uninstall not supported"},
				"runDebug":                runDebugCmd,
				"forwardPort":             forwardCmd,
				"forwardPortSuccessRegex": "forwarded",
			},
		},
	}

	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, output, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}
