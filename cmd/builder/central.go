package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/MobAI-App/ios-builder/internal/security"
	"github.com/MobAI-App/ios-builder/internal/snapshot"
	"github.com/spf13/cobra"
)

var centralCmd = &cobra.Command{
	Use:   "central",
	Short: "Configure and diagnose the public central-builder backend",
}

var centralSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure this private project to use one public builder",
	RunE:  runCentralSetup,
}

var centralDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Verify central-builder configuration without printing secrets",
	RunE:  runCentralDoctor,
}

func init() {
	rootCmd.AddCommand(centralCmd)
	centralCmd.AddCommand(centralSetupCmd, centralDoctorCmd)
	centralSetupCmd.Flags().String("builder", "", "Public builder repository as OWNER/REPO (required)")
	centralSetupCmd.Flags().StringP("remote", "r", "origin", "Private source git remote")
	centralSetupCmd.Flags().StringP("project", "p", "", "Project name (defaults to directory name)")
	centralSetupCmd.Flags().String("ios-path", "", "Relative path to the iOS project (auto-detected)")
	centralSetupCmd.Flags().String("scheme", "", "Xcode scheme (auto-detected when empty)")
	centralSetupCmd.Flags().String("configuration", "Debug", "Build configuration: Debug or Release")
	centralDoctorCmd.Flags().StringP("remote", "r", "origin", "Private source git remote")
	centralDoctorCmd.Flags().Bool("testflight", false, "Also verify apple-production metadata without reading secret values")
}

func runCentralSetup(cmd *cobra.Command, _ []string) error {
	builderName, _ := cmd.Flags().GetString("builder")
	parts := strings.Split(builderName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return errors.New("--builder OWNER/REPO is required")
	}
	remote, _ := cmd.Flags().GetString("remote")
	sourceOwner, sourceRepo, err := detectGitHubRepo(remote)
	if err != nil {
		return fmt.Errorf("detect private source repository: %w", err)
	}
	project, _ := cmd.Flags().GetString("project")
	if project == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		project = filepath.Base(cwd)
	}
	iosPath, _ := cmd.Flags().GetString("ios-path")
	if iosPath == "" {
		iosPath, _ = detectIOSPath()
	}
	scheme, _ := cmd.Flags().GetString("scheme")
	configuration, _ := cmd.Flags().GetString("configuration")
	recipient, err := security.EnsureIdentity()
	if err != nil {
		return fmt.Errorf("initialize local AGE identity: %w", err)
	}

	cfg := &config.Config{
		Project:     project,
		Platform:    "ios",
		Backend:     config.BackendCentral,
		GitHub:      config.GitHubConfig{Owner: sourceOwner, Repo: sourceRepo},
		Builder:     config.BuilderConfig{Owner: parts[0], Repo: parts[1], Workflow: config.DefaultWorkflow},
		Security:    config.SecurityConfig{Recipient: recipient},
		IOS:         config.IOSConfig{Path: iosPath, Scheme: scheme, Configuration: configuration},
		ReactNative: config.ReactNativeConfig{Expo: isExpoProject()},
	}
	if isFlutterProject() {
		cfg.Flutter.Version = getLocalFlutterVersion()
	}
	if isKMPProject() {
		cfg.KMP.JDKVersion = "17"
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.NewManager().Save(cfg); err != nil {
		return fmt.Errorf("write builder.json: %w", err)
	}

	fmt.Printf("Configured %s/%s to build through %s/%s.\n", sourceOwner, sourceRepo, parts[0], parts[1])
	fmt.Println("No workflow or private source was written to the public builder.")
	printGitHubAppSetup(parts[0], parts[1])
	fmt.Println("After the one-time GitHub App setup, run: builder central doctor")
	return nil
}

func printGitHubAppSetup(owner, repo string) {
	fmt.Println()
	fmt.Println("One-time GitHub App settings (manual browser step):")
	fmt.Println("  App name: ios-cloud-builder-<your-account>")
	fmt.Println("  Homepage URL: https://github.com/" + owner + "/" + repo)
	fmt.Println("  Webhook: inactive")
	fmt.Println("  Repository permissions: Contents = Read-only; Metadata = Read-only (implicit)")
	fmt.Println("  All other repository and organization permissions: No access")
	fmt.Println("  Installation: Only on this account; choose `Only select repositories`")
	fmt.Println("  Generate one private key, then configure the public builder:")
	fmt.Println("    Repository variable APP_CLIENT_ID = the App client ID")
	fmt.Println("    Repository secret   APP_PRIVATE_KEY = the complete generated PEM")
	fmt.Println("  Delete the downloaded PEM after the repository secret is verified, or store it in your password manager.")
}

func runCentralDoctor(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if !cfg.IsCentral() {
		return errors.New("builder.json uses the repository backend; run `builder central setup --builder OWNER/REPO`")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	remote, _ := cmd.Flags().GetString("remote")
	type check struct {
		name string
		fn   func(context.Context) error
	}
	var authSource string
	var client *github.Client
	var publicRepository *github.Repository
	checks := []check{
		{"git executable", func(context.Context) error { _, err := exec.LookPath("git"); return err }},
		{"configuration", func(context.Context) error { return cfg.Validate() }},
		{"local AGE identity", func(context.Context) error {
			_, err := security.LoadIdentity()
			if err != nil {
				return err
			}
			recipient, err := security.Recipient()
			if err != nil {
				return err
			}
			if recipient != cfg.Security.Recipient {
				return errors.New("builder.json recipient does not match the local identity")
			}
			return nil
		}},
		{"GitHub authentication", func(context.Context) error {
			token, source, err := auth.GetTokenWithSource()
			if err != nil {
				return err
			}
			authSource, client = source, github.NewClient(token)
			return nil
		}},
		{"private source API access", func(ctx context.Context) error {
			_, err := client.GetRepository(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo)
			return err
		}},
		{"public builder API access", func(ctx context.Context) error {
			var err error
			publicRepository, err = client.GetRepository(ctx, cfg.Builder.Owner, cfg.Builder.Repo)
			return err
		}},
		{"central workflow", func(ctx context.Context) error {
			return client.GetWorkflow(ctx, cfg.Builder.Owner, cfg.Builder.Repo, cfg.Builder.Workflow)
		}},
		{"APP_CLIENT_ID variable", func(ctx context.Context) error {
			_, err := client.GetActionVariable(ctx, cfg.Builder.Owner, cfg.Builder.Repo, "APP_CLIENT_ID")
			return err
		}},
		{"APP_PRIVATE_KEY secret metadata", func(ctx context.Context) error {
			_, err := client.GetActionSecret(ctx, cfg.Builder.Owner, cfg.Builder.Repo, "APP_PRIVATE_KEY")
			return err
		}},
		{"source git remote", func(ctx context.Context) error {
			return snapshot.VerifyRemote(ctx, remote, cfg.GitHub.Owner, cfg.GitHub.Repo)
		}},
		{"snapshot push permission (dry run)", func(ctx context.Context) error {
			ref := snapshot.Ref("00000000-0000-4000-8000-000000000000")
			process := exec.CommandContext(ctx, "git", "push", "--dry-run", remote, "HEAD:"+ref)
			process.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			if out, err := process.CombinedOutput(); err != nil {
				return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		}},
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	testFlight, _ := cmd.Flags().GetBool("testflight")
	if testFlight {
		checks = append(checks,
			check{"APPLE_SIGNING_RECIPIENT variable", func(ctx context.Context) error {
				_, err := client.GetActionVariable(ctx, cfg.Builder.Owner, cfg.Builder.Repo, "APPLE_SIGNING_RECIPIENT")
				return err
			}},
			check{"apple-production protection rules", func(ctx context.Context) error {
				environment, err := client.GetEnvironment(ctx, cfg.Builder.Owner, cfg.Builder.Repo, "apple-production")
				if err != nil {
					return err
				}
				policies, err := client.GetDeploymentBranchPolicies(ctx, cfg.Builder.Owner, cfg.Builder.Repo, "apple-production")
				if err != nil {
					return err
				}
				if publicRepository == nil || publicRepository.DefaultRef == "" {
					return errors.New("public builder default branch metadata is missing")
				}
				return github.ValidateProductionEnvironment(environment, policies, publicRepository.DefaultRef)
			}},
			check{"APPLE_TEAM_ID environment variable", func(ctx context.Context) error {
				_, err := client.GetEnvironmentActionVariable(ctx, cfg.Builder.Owner, cfg.Builder.Repo, "apple-production", "APPLE_TEAM_ID")
				return err
			}},
		)
		for _, name := range []string{
			"APPLE_SIGNING_AGE_IDENTITY", "APPLE_DISTRIBUTION_P12", "APPLE_DISTRIBUTION_P12_PASSWORD",
			"APPLE_PROVISIONING_PROFILE", "ASC_API_KEY_P8", "ASC_KEY_ID",
		} {
			secretName := name
			checks = append(checks, check{secretName + " environment secret", func(ctx context.Context) error {
				_, err := client.GetEnvironmentActionSecret(ctx, cfg.Builder.Owner, cfg.Builder.Repo, "apple-production", secretName)
				return err
			}})
		}
	}
	for _, item := range checks {
		if err := item.fn(ctx); err != nil {
			fmt.Printf("FAIL  %s: %v\n", item.name, err)
			return errors.New("central builder doctor found a problem")
		}
		if item.name == "GitHub authentication" {
			fmt.Printf("OK    %s (%s)\n", item.name, authSource)
		} else {
			fmt.Printf("OK    %s\n", item.name)
		}
	}
	if testFlight {
		fmt.Println("Central builder and TestFlight Environment metadata are ready.")
	} else {
		fmt.Println("Central builder is ready.")
	}
	return nil
}
