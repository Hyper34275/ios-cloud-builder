// Package iosbox provides Docker-based iOS builds using the iosbox toolchain.
// It builds Flutter iOS apps locally via Docker instead of GitHub Actions.
package iosbox

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/manifoldco/promptui"
)

const (
	// DockerImage is the base iosbox Docker image
	DockerImage = "mobaiapp/iosbox"

	// SDKVolume is the Docker volume for the iosbox SDK (extracted Xcode)
	SDKVolume = "iosbox-sdk"

	// XcodeDownloadURL is where users download Xcode xip files
	XcodeDownloadURL = "https://developer.apple.com/download/all/"
)

// BuildOptions holds configuration for an iosbox build
type BuildOptions struct {
	ProjectDir     string
	OutputDir      string
	FlutterVersion string // If set, overrides local detection
}

// BuildResult holds the result of an iosbox build
type BuildResult struct {
	IPAPath  string
	Duration time.Duration
}

// imageTag returns the full Docker image tag for a Flutter version
func imageTag(flutterVersion string) string {
	return fmt.Sprintf("%s:flutter-%s", DockerImage, flutterVersion)
}

// checkDocker verifies Docker is installed and running
func checkDocker() error {
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Docker is not available. Install it from: https://docs.docker.com/get-docker/")
	}
	return nil
}

// getFlutterVersion gets the local Flutter version. Returns error if Flutter is not installed.
func getFlutterVersion() (string, error) {
	cmd := exec.Command("flutter", "--version", "--machine")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("Flutter is not installed. Install it from: https://docs.flutter.dev/get-started/install")
	}

	var result struct {
		FrameworkVersion string `json:"frameworkVersion"`
	}
	if err := json.Unmarshal(output, &result); err != nil || result.FrameworkVersion == "" {
		return "", fmt.Errorf("failed to detect Flutter version")
	}
	return result.FrameworkVersion, nil
}

// checkSDKVolume checks if the iosbox-sdk Docker volume exists
func checkSDKVolume() bool {
	cmd := exec.Command("docker", "volume", "inspect", SDKVolume)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// pullImage pulls the iosbox Docker image for the given Flutter version
func pullImage(flutterVersion string) error {
	tag := imageTag(flutterVersion)
	fmt.Printf("Pulling %s...\n", tag)
	cmd := exec.Command("docker", "pull", "--platform", "linux/amd64", tag)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to pull %s — this Flutter version may not be supported by iosbox", tag)
	}
	return nil
}

// promptXcodePath asks the user to download Xcode and provide the .xip path
func promptXcodePath() (string, error) {
	fmt.Println()
	fmt.Println("iosbox SDK not found. You need to set up Xcode SDK first.")
	fmt.Printf("1. Download Xcode 26.3.xip from: %s\n", XcodeDownloadURL)
	fmt.Println("2. Provide the path to the downloaded .xip file below")
	fmt.Println()

	prompt := promptui.Prompt{
		Label: "Path to Xcode .xip file",
		Validate: func(input string) error {
			input = strings.TrimSpace(input)
			if input == "" {
				return fmt.Errorf("path is required")
			}
			if !strings.HasSuffix(input, ".xip") {
				return fmt.Errorf("file must be a .xip archive")
			}
			if _, err := os.Stat(input); err != nil {
				return fmt.Errorf("file not found: %s", input)
			}
			return nil
		},
	}
	return prompt.Run()
}

// runSetup runs the iosbox setup command to extract the Xcode SDK
func runSetup(xcodePath string, flutterVersion string) error {
	absPath, err := filepath.Abs(xcodePath)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	tag := imageTag(flutterVersion)
	fmt.Println()
	fmt.Println("Setting up iosbox SDK (this may take a while)...")

	cmd := exec.Command("docker", "run", "--rm", "--platform", "linux/amd64",
		"-v", absPath+":/workspace/Xcode.xip",
		"-v", SDKVolume+":/root/.iosbox",
		tag,
		"iosbox", "setup", "/workspace/Xcode.xip",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("iosbox setup failed: %w", err)
	}

	fmt.Println("iosbox SDK setup complete!")
	return nil
}

// runBuild runs the iosbox build command
func runBuild(opts BuildOptions) (*BuildResult, error) {
	startTime := time.Now()

	projectDir, err := filepath.Abs(opts.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project path: %w", err)
	}

	projectName := filepath.Base(projectDir)
	tag := imageTag(opts.FlutterVersion)

	fmt.Println()
	fmt.Printf("🔨 Building with iosbox (Flutter %s)...\n", opts.FlutterVersion)
	fmt.Println()

	cmd := exec.Command("docker", "run", "--rm", "--platform", "linux/amd64",
		"-v", SDKVolume+":/root/.iosbox",
		"-v", projectDir+":/project",
		"-v", fmt.Sprintf("iosbox-swift-cache-%s:/root/.cache/org.swift.swiftpm", projectName),
		"-v", fmt.Sprintf("iosbox-build-cache-%s:/tmp/iosbox-native-build", projectName),
		tag,
		"iosbox", "build", "/project",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("iosbox build failed: %w", err)
	}

	ipaSource := filepath.Join(projectDir, "build", "iosbox", "Runner.ipa")
	if _, err := os.Stat(ipaSource); err != nil {
		return nil, fmt.Errorf("build completed but IPA not found at %s", ipaSource)
	}

	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	ipaDest := filepath.Join(opts.OutputDir, "Runner.ipa")
	if err := copyFile(ipaSource, ipaDest); err != nil {
		return nil, fmt.Errorf("failed to copy IPA: %w", err)
	}

	return &BuildResult{
		IPAPath:  ipaDest,
		Duration: time.Since(startTime),
	}, nil
}

// Build orchestrates the full iosbox build flow:
// check docker, check flutter, check SDK, pull image, setup if needed, build
func Build(opts BuildOptions) (*BuildResult, error) {
	// Step 1: Check Docker
	fmt.Println("Checking Docker...")
	if err := checkDocker(); err != nil {
		return nil, err
	}
	fmt.Println("  Docker is available")

	// Step 2: Check Flutter version (must be installed locally)
	fmt.Println("Checking Flutter...")
	flutterVersion := opts.FlutterVersion
	if flutterVersion == "" {
		v, err := getFlutterVersion()
		if err != nil {
			return nil, err
		}
		flutterVersion = v
	}
	opts.FlutterVersion = flutterVersion
	fmt.Printf("  Flutter version: %s\n", flutterVersion)

	// Step 3: Check iosbox-sdk volume
	hasSDK := checkSDKVolume()
	if hasSDK {
		fmt.Println("  iosbox SDK volume found")
	}

	// Step 4: Pull Docker image
	fmt.Println()
	if err := pullImage(flutterVersion); err != nil {
		return nil, err
	}

	// Step 5: If no SDK, prompt for Xcode and run setup
	if !hasSDK {
		xcodePath, err := promptXcodePath()
		if err != nil {
			return nil, fmt.Errorf("setup cancelled: %w", err)
		}
		if err := runSetup(xcodePath, flutterVersion); err != nil {
			return nil, err
		}
	}

	// Step 6: Run build
	return runBuild(opts)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	w := bufio.NewWriter(out)
	if _, err := w.ReadFrom(in); err != nil {
		return err
	}
	return w.Flush()
}
