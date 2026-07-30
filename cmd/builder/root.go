package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/build"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/workflow"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var verbose bool

var rootCmd = &cobra.Command{
	Use:   "builder",
	Short: "Build iOS apps remotely using GitHub Actions",
	Long: `Builder sets up GitHub Actions workflows to build iOS apps remotely.
Perfect for developers on Windows/Linux who need to build iOS IPAs.`,
	SilenceUsage: true,
	Version:      version,
}

func initConfig() {
	viper.SetConfigName("builder")
	viper.SetConfigType("json")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()
	// Ignore error: config file is optional
	_ = viper.ReadInConfig()
}

func getGitHubClient() (*github.Client, error) {
	token, err := auth.GetToken()
	if err != nil {
		return nil, fmt.Errorf("not authenticated. Run: builder auth github")
	}
	return github.NewClient(token), nil
}

func loadConfig() (*config.Config, error) {
	mgr := config.NewManager()
	cfg, err := mgr.Load()
	if err != nil {
		if err == config.ErrConfigNotFound {
			return nil, fmt.Errorf("builder.json not found. Run: builder init")
		}
		return nil, err
	}
	return cfg, nil
}

// stdinReader is shared so bytes buffered by one prompt are not lost by the next.
var stdinReader = bufio.NewReader(os.Stdin)

// promptString reads a line of free text from stdin.
//
// It deliberately avoids promptui here: promptui redraws the entire prompt on
// every keystroke and its screen buffer assumes the rendered prompt occupies a
// single terminal line. Pasting a value long enough to wrap breaks that
// assumption, so each redraw strands a copy of the prompt on screen and the
// visible text is garbled. Reading the line in the terminal's normal cooked
// mode lets the terminal handle echo, wrapping and paste on its own.
func promptString(label, defaultVal string) (string, error) {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}

	line, err := stdinReader.ReadString('\n')
	if err != nil && !(err == io.EOF && line != "") {
		return "", err
	}

	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return defaultVal, nil
	}
	return line, nil
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize iOS builds for this repository",
	Long: `Sets up GitHub Actions workflow for iOS builds in the current repository.

This command:
- Detects your GitHub repository from git remote
- Adds the iOS build workflow to .github/workflows/
- Creates builder.json configuration`,
	RunE: runInit,
}

func isFlutterProject() bool {
	_, err := os.Stat("pubspec.yaml")
	return err == nil
}

func isExpoProject() bool {
	data, err := os.ReadFile("package.json")
	if err != nil {
		return false
	}
	return strings.Contains(string(data), `"expo"`)
}

func getLocalFlutterVersion() string {
	cmd := exec.Command("flutter", "--version", "--machine")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	// Parse JSON output: {"frameworkVersion":"3.24.0",...}
	var result struct {
		FrameworkVersion string `json:"frameworkVersion"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return ""
	}
	return result.FrameworkVersion
}

func detectIOSPath() (string, string) {
	patterns := []struct {
		path      string
		framework string
	}{
		{"ios", "React Native/Expo"},
		{"platforms/ios", "Cordova/Ionic"},
	}

	for _, p := range patterns {
		if entries, err := os.ReadDir(p.path); err == nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".xcworkspace") || strings.HasSuffix(e.Name(), ".xcodeproj") {
					return p.path, p.framework
				}
			}
		}
	}

	if entries, err := os.ReadDir("."); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".xcworkspace") || strings.HasSuffix(e.Name(), ".xcodeproj") {
				return "", "Native iOS"
			}
		}
	}

	return "", ""
}

func detectGitHubRepo(remoteName string) (owner, repo string, err error) {
	// Try to get GitHub remote URL from git
	cmd := exec.Command("git", "remote", "get-url", remoteName)
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("not a git repository or no '%s' remote", remoteName)
	}

	remoteURL := strings.TrimSpace(string(output))

	// Parse GitHub URL formats:
	// https://github.com/owner/repo.git
	// git@github.com:owner/repo.git
	// git@github-alias:owner/repo.git (SSH config aliases)
	// https://github.com/owner/repo

	remoteURL = strings.TrimSuffix(remoteURL, ".git")

	if path, found := strings.CutPrefix(remoteURL, "https://github.com/"); found {
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			return parts[0], parts[1], nil
		}
	}

	if strings.HasPrefix(remoteURL, "git@") {
		if colonIdx := strings.Index(remoteURL, ":"); colonIdx > 0 {
			path := remoteURL[colonIdx+1:]
			parts := strings.Split(path, "/")
			if len(parts) >= 2 {
				return parts[0], parts[1], nil
			}
		}
	}

	return "", "", fmt.Errorf("could not parse GitHub URL from: %s", remoteURL)
}

func runInit(cmd *cobra.Command, args []string) error {
	fmt.Println("Builder - iOS Build Setup")
	fmt.Println()

	// Get remote name from flag
	remoteName, _ := cmd.Flags().GetString("remote")

	// Detect GitHub repo from git remote
	githubOwner, repoName, err := detectGitHubRepo(remoteName)
	if err != nil {
		return fmt.Errorf("failed to detect GitHub repository: %w\nMake sure you're in a git repository with a GitHub remote", err)
	}

	fmt.Printf("Detected repository: %s/%s (from remote '%s')\n", githubOwner, repoName, remoteName)
	fmt.Println()

	// Get project name
	projectName, _ := cmd.Flags().GetString("project")
	if projectName == "" {
		cwd, _ := os.Getwd()
		defaultProject := filepath.Base(cwd)
		projectName, err = promptString("Project name", defaultProject)
		if err != nil {
			return err
		}
	}

	// Detect iOS path
	iosPath, _ := cmd.Flags().GetString("ios-path")
	scheme, _ := cmd.Flags().GetString("scheme")

	if iosPath == "" {
		detectedPath, framework := detectIOSPath()
		if detectedPath != "" {
			fmt.Printf("Detected %s project (iOS at '%s')\n", framework, detectedPath)
			confirmPrompt := promptui.Prompt{
				Label:     "Use this path",
				IsConfirm: true,
			}
			_, err := confirmPrompt.Run()
			if err == nil {
				iosPath = detectedPath
			}
		} else if framework != "" {
			fmt.Printf("Detected %s project\n", framework)
		}

		if iosPath == "" && framework == "" {
			fmt.Println("No iOS project detected in current directory.")
			fmt.Println("If this is a hybrid app (React Native, Flutter, etc.),")
			iosPath, _ = promptString("Path to iOS folder (leave empty for root)", "")
		}
	}

	// Detect Flutter and prompt for version
	var flutterVersion string
	if isFlutterProject() {
		fmt.Println()
		fmt.Println("Detected Flutter project")
		localVersion := getLocalFlutterVersion()
		if localVersion != "" {
			fmt.Printf("Local Flutter version: %s\n", localVersion)
		}
		flutterVersion, err = promptString("Flutter version for builds (leave empty for latest)", localVersion)
		if err != nil {
			return err
		}
	}

	fmt.Println()
	fmt.Printf("Project:    %s\n", projectName)
	fmt.Printf("Repository: %s/%s\n", githubOwner, repoName)
	if iosPath != "" {
		fmt.Printf("iOS Path:   %s\n", iosPath)
	}
	if flutterVersion != "" {
		fmt.Printf("Flutter:    %s\n", flutterVersion)
	}
	fmt.Println()

	// Create workflow file locally
	fmt.Println("Creating workflow file...")
	workflowDir := ".github/workflows"
	if err := os.MkdirAll(workflowDir, 0755); err != nil {
		return fmt.Errorf("failed to create workflow directory: %w", err)
	}

	workflowContent, err := workflow.GetWorkflowTemplate()
	if err != nil {
		return fmt.Errorf("failed to get workflow template: %w", err)
	}

	workflowPath := filepath.Join(workflowDir, "ios-build.yml")
	if err := os.WriteFile(workflowPath, workflowContent, 0644); err != nil {
		return fmt.Errorf("failed to write workflow file: %w", err)
	}
	fmt.Printf("  Created: %s\n", workflowPath)

	// Save config
	cfg := &config.Config{
		Project:  projectName,
		Platform: "ios",
		GitHub: config.GitHubConfig{
			Owner: githubOwner,
			Repo:  repoName,
		},
		IOS: config.IOSConfig{
			Path:   iosPath,
			Scheme: scheme,
		},
		Flutter: config.FlutterConfig{
			Version: flutterVersion,
		},
		ReactNative: config.ReactNativeConfig{
			Expo: isExpoProject(),
		},
	}

	fmt.Println("Creating builder.json...")
	mgr := config.NewManager()
	if err := mgr.Save(cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Println("  Created: builder.json")

	fmt.Println()
	fmt.Println("Setup complete!")
	fmt.Println()

	// Ask to commit and push
	commitPrompt := promptui.Prompt{
		Label:     "Commit and push workflow",
		IsConfirm: true,
	}
	_, commitErr := commitPrompt.Run()

	if commitErr == nil {
		fmt.Println()
		fmt.Println("Committing and pushing...")

		// Git add
		addCmd := exec.Command("git", "add", ".github/workflows/ios-build.yml", "builder.json")
		if output, err := addCmd.CombinedOutput(); err != nil {
			fmt.Printf("  Warning: git add failed: %s\n", strings.TrimSpace(string(output)))
		} else {
			fmt.Println("  Added files to staging")
		}

		// Git commit
		commitCmd := exec.Command("git", "commit", "-m", "Add iOS build workflow")
		if output, err := commitCmd.CombinedOutput(); err != nil {
			outputStr := strings.TrimSpace(string(output))
			if strings.Contains(outputStr, "nothing to commit") {
				fmt.Println("  Nothing to commit (already committed)")
			} else {
				fmt.Printf("  Warning: git commit failed: %s\n", outputStr)
			}
		} else {
			fmt.Println("  Committed changes")
		}

		// Git push
		pushCmd := exec.Command("git", "push")
		if output, err := pushCmd.CombinedOutput(); err != nil {
			fmt.Printf("  Warning: git push failed: %s\n", strings.TrimSpace(string(output)))
		} else {
			fmt.Println("  Pushed to remote")
		}
		fmt.Println()
	}

	// Ask to run build
	buildPrompt := promptui.Prompt{
		Label:     "Run build now",
		IsConfirm: true,
	}
	_, buildErr := buildPrompt.Run()

	if buildErr == nil {
		fmt.Println()
		// Run build with default options (use signing if configured)
		return runBuild(context.Background(), cfg, "dist", 30*time.Minute, false)
	}

	fmt.Println()
	fmt.Println("To build later, run:")
	fmt.Println("  builder ios build")
	fmt.Println()

	return nil
}

var iosCmd = &cobra.Command{
	Use:   "ios",
	Short: "iOS build commands",
}

var iosBuildCmd = &cobra.Command{
	Use:   "build",
	Short: "Trigger a remote iOS build",
	Long:  `Triggers the iOS build workflow on GitHub Actions and downloads the IPA artifact.`,
	RunE:  runIOSBuild,
}

func init() {
	// Root command setup
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(iosCmd)
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(signingCmd)
	rootCmd.AddCommand(devCmd)
	rootCmd.AddCommand(mobaiCmd)

	// Init command flags
	initCmd.Flags().StringP("project", "p", "", "Project name (defaults to directory name)")
	initCmd.Flags().String("ios-path", "", "Path to iOS project (e.g., 'ios' for React Native)")
	initCmd.Flags().String("scheme", "", "Xcode scheme to build (auto-detected if empty)")
	initCmd.Flags().StringP("remote", "r", "origin", "Git remote name to use for GitHub repository")

	// iOS build command flags
	iosBuildCmd.Flags().StringP("output", "o", "dist", "Output directory for IPA")
	iosBuildCmd.Flags().Duration("timeout", 30*time.Minute, "Build timeout")
	iosBuildCmd.Flags().Bool("unsigned", false, "Build unsigned IPA (skip code signing even if configured)")
	iosCmd.AddCommand(iosBuildCmd)
}

func runIOSBuild(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	outputDir, _ := cmd.Flags().GetString("output")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	unsigned, _ := cmd.Flags().GetBool("unsigned")

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	return runBuild(ctx, cfg, outputDir, timeout, unsigned)
}

func runBuild(ctx context.Context, cfg *config.Config, outputDir string, timeout time.Duration, unsigned bool) error {
	ghClient, err := getGitHubClient()
	if err != nil {
		return err
	}

	coordinator := build.NewCoordinator(cfg, ghClient)

	result, err := coordinator.Build(ctx, build.BuildOptions{
		OutputDir: outputDir,
		Timeout:   timeout,
		Unsigned:  unsigned,
	})

	if err != nil {
		return err
	}

	fmt.Printf("IPA: %s\n", result.IPAPath)
	fmt.Printf("Workflow: %s\n", result.WorkflowURL)

	return nil
}
