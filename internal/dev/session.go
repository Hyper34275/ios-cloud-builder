// Package dev provides development session management for Flutter hot reload.
package dev

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/mobai"
	"github.com/gorilla/websocket"
	"github.com/manifoldco/promptui"
	"howett.net/plist"
)

// Session manages a Flutter development session with MobAI.
type Session struct {
	mobai       *mobai.Client
	mobaiURL    string
	deviceID    string
	ipaPath     string
	bundleID    string
	debugConn   *websocket.Conn
	skipInstall bool
}

// NewSession creates a new development session.
func NewSession(mobaiURL, deviceID, ipaPath string) *Session {
	return &Session{
		mobai:    mobai.NewClient(mobaiURL),
		mobaiURL: mobaiURL,
		deviceID: deviceID,
		ipaPath:  ipaPath,
	}
}

// SetSkipInstall configures the session to skip installation.
func (s *Session) SetSkipInstall(skip bool, bundleID string) {
	s.skipInstall = skip
	if bundleID != "" {
		s.bundleID = bundleID
	}
}

// FindIPA lists IPAs in distDir and lets user select if multiple.
func FindIPA(distDir string) (string, error) {
	if distDir == "" {
		distDir = "dist"
	}

	matches, err := filepath.Glob(filepath.Join(distDir, "*.ipa"))
	if err != nil {
		return "", fmt.Errorf("search IPA: %w", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no IPA in %s - run 'builder ios build' first", distDir)
	}

	if len(matches) == 1 {
		return matches[0], nil
	}

	names := make([]string, len(matches))
	for i, p := range matches {
		names[i] = filepath.Base(p)
	}

	prompt := promptui.Select{
		Label: "Select IPA",
		Items: names,
	}

	idx, _, err := prompt.Run()
	if err != nil {
		return "", err
	}

	return matches[idx], nil
}

// EnsureCustomDevice ensures Flutter custom_devices.json has mobai-ios configured.
// If mobaiURL is provided and we're on WSL, it will be included in the commands.
func EnsureCustomDevice(mobaiURL string) error {
	if err := exec.Command("flutter", "config", "--enable-custom-devices").Run(); err != nil {
		return fmt.Errorf("failed to enable custom devices - run 'flutter config --enable-custom-devices' manually first: %w", err)
	}

	var configPath string
	if runtime.GOOS == "windows" {
		// Windows: %APPDATA%\.flutter_custom_devices.json
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return fmt.Errorf("APPDATA environment variable not set")
		}
		configPath = filepath.Join(appData, ".flutter_custom_devices.json")
	} else {
		// Linux/macOS: ~/.config/flutter/custom_devices.json
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

// Start runs the full dev session: install, launch, attach.
func (s *Session) Start(ctx context.Context) error {
	if err := EnsureCustomDevice(s.mobaiURL); err != nil {
		fmt.Printf("Warning: could not configure custom device: %v\n", err)
	}

	if err := s.connectDevice(ctx); err != nil {
		return err
	}
	if !s.skipInstall {
		if err := s.installApp(ctx); err != nil {
			return err
		}
	} else {
		fmt.Printf("Skipping install, using bundle ID: %s\n", s.bundleID)
	}
	debugURL, outputChan, err := s.launchAndFindDebugURL(ctx)
	if err != nil {
		return err
	}
	return s.waitWithDebugURL(ctx, debugURL, outputChan)
}

// Stop cleans up resources.
func (s *Session) Stop() {
	if s.debugConn != nil {
		_ = s.debugConn.Close()
	}
}

func (s *Session) connectDevice(ctx context.Context) error {
	fmt.Println("Connecting to MobAI...")

	allDevices, err := s.mobai.ListDevices(ctx)
	if err != nil {
		return fmt.Errorf("connect to MobAI: %w", err)
	}

	// Filter to physical iOS devices only (no simulators)
	var devices []mobai.Device
	for _, d := range allDevices {
		if !d.Virtual {
			devices = append(devices, d)
		}
	}
	if len(devices) == 0 {
		return fmt.Errorf("no physical iOS devices connected")
	}

	var device *mobai.Device
	if s.deviceID != "" {
		for i := range devices {
			if devices[i].ID == s.deviceID {
				device = &devices[i]
				break
			}
		}
		if device == nil {
			return fmt.Errorf("device %s not found", s.deviceID)
		}
	} else if len(devices) == 1 {
		device = &devices[0]
	} else {
		names := make([]string, len(devices))
		for i, d := range devices {
			names[i] = d.Name
		}

		prompt := promptui.Select{
			Label: "Select device",
			Items: names,
		}
		idx, _, err := prompt.Run()
		if err != nil {
			return err
		}
		device = &devices[idx]
	}

	s.deviceID = device.ID
	fmt.Printf("Using device: %s\n", device.Name)
	return nil
}

func (s *Session) installApp(ctx context.Context) error {
	absPath, err := filepath.Abs(s.ipaPath)
	if err != nil {
		return fmt.Errorf("get absolute path: %w", err)
	}

	absPath = toWindowsPathIfWSL(absPath)

	req := mobai.InstallAppRequest{Path: absPath}

	resignPrompt := promptui.Select{
		Label: "Resign app",
		Items: []string{"No", "Yes"},
	}
	idx, _, err := resignPrompt.Run()
	if err != nil {
		return err
	}

	if idx == 1 {
		req.Resign = true

		appleIDPrompt := promptui.Prompt{Label: "Apple ID"}
		req.AppleID, err = appleIDPrompt.Run()
		if err != nil {
			return err
		}

		passwordPrompt := promptui.Prompt{Label: "Password", Mask: '*'}
		req.Password, err = passwordPrompt.Run()
		if err != nil {
			return err
		}
	}

	fmt.Println("Installing app...")
	resp, err := s.mobai.InstallApp(ctx, s.deviceID, req)
	if err != nil {
		return fmt.Errorf("install app: %w", err)
	}

	s.bundleID = resp.Data.BundleID
	if s.bundleID == "" {
		detected := extractBundleIDFromIPA(absPath)
		bundlePrompt := promptui.Prompt{Label: "Bundle ID", Default: detected}
		s.bundleID, err = bundlePrompt.Run()
		if err != nil {
			return err
		}
	}

	fmt.Printf("Installed: %s\n", s.bundleID)
	return nil
}

func extractBundleIDFromIPA(ipaPath string) string {
	r, err := zip.OpenReader(ipaPath)
	if err != nil {
		return ""
	}
	defer func() { _ = r.Close() }()

	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "Payload/") && strings.HasSuffix(f.Name, ".app/Info.plist") {
			rc, err := f.Open()
			if err != nil {
				return ""
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return ""
			}

			var info struct {
				BundleID string `plist:"CFBundleIdentifier"`
			}
			if _, err := plist.Unmarshal(data, &info); err != nil {
				return ""
			}
			return info.BundleID
		}
	}
	return ""
}

func (s *Session) launchAndFindDebugURL(ctx context.Context) (string, <-chan mobai.DebugOutput, error) {
	fmt.Println("Launching app with debugger...")

	outputChan, conn, err := s.mobai.DebugStream(ctx, s.deviceID, s.bundleID)
	if err != nil {
		if strings.Contains(err.Error(), "Error code: 2") {
			return "", nil, fmt.Errorf("launch failed: developer not trusted. On device go to Settings > General > VPN & Device Management and trust the developer")
		}
		return "", nil, fmt.Errorf("debug stream: %w", err)
	}
	s.debugConn = conn

	fmt.Println("Waiting for debug service...")

	re := regexp.MustCompile(`(?:Observatory|Dart VM service)[^h]*(http://[^\s]+)`)
	timeout := time.After(30 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-timeout:
			return "", nil, fmt.Errorf("timeout waiting for debug service - is this a debug build?")
		case output, ok := <-outputChan:
			if !ok {
				return "", nil, fmt.Errorf("debug stream closed")
			}
			if output.Type == "error" {
				return "", nil, fmt.Errorf("debug error: %s", output.Message)
			}
			if m := re.FindStringSubmatch(output.Data); len(m) >= 2 {
				fmt.Printf("Found debug service: %s\n", m[1])
				return m[1], outputChan, nil
			}
		}
	}
}

func (s *Session) waitWithDebugURL(ctx context.Context, debugURL string, outputChan <-chan mobai.DebugOutput) error {
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
	cmd.Env = append(os.Environ(), "MOBAI_DEVICE_ID="+s.deviceID)

	err := cmd.Run()
	if err != nil {
		return &RuntimeError{Err: err}
	}
	return nil
}

// RuntimeError wraps runtime errors to distinguish from CLI errors
type RuntimeError struct {
	Err error
}

func (e *RuntimeError) Error() string {
	return e.Err.Error()
}

func isWSL() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
}

// toWindowsPathIfWSL converts a WSL path to Windows path if running in WSL
func toWindowsPathIfWSL(path string) string {
	if !isWSL() {
		return path
	}

	cmd := exec.Command("wslpath", "-w", path)
	out, err := cmd.Output()
	if err != nil {
		return path
	}

	return strings.TrimSpace(string(out))
}
