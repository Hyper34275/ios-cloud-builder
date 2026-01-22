package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/MobAI-App/ios-builder/internal/config"
	"github.com/MobAI-App/ios-builder/internal/github"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var signingCmd = &cobra.Command{
	Use:   "signing",
	Short: "Code signing commands",
}

var signingSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Set up code signing for iOS builds",
	Long: `Uploads your iOS signing certificate and provisioning profile to GitHub Secrets.

This command will:
- Read your .p12 certificate file
- Read your .mobileprovision provisioning profile
- Base64 encode and encrypt them
- Upload them as GitHub repository secrets:
  - IOS_CERTIFICATE
  - IOS_CERTIFICATE_PASSWORD
  - IOS_PROVISIONING_PROFILE

After setup, builds will be signed automatically.`,
	RunE: runSigningSetup,
}

func init() {
	signingCmd.AddCommand(signingSetupCmd)

	signingSetupCmd.Flags().StringP("certificate", "c", "", "Path to .p12 certificate file")
	signingSetupCmd.Flags().StringP("profile", "p", "", "Path to .mobileprovision file")
}

func runSigningSetup(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	ghClient, err := getGitHubClient()
	if err != nil {
		return err
	}

	// Get certificate path
	certPath, _ := cmd.Flags().GetString("certificate")
	if certPath == "" {
		prompt := promptui.Prompt{Label: "Path to .p12 certificate file"}
		certPath, err = prompt.Run()
		if err != nil {
			return err
		}
	}

	// Validate certificate file
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read certificate: %w", err)
	}
	fmt.Printf("Certificate: %s (%.1f KB)\n", certPath, float64(len(certData))/1024)

	// Get provisioning profile path
	profilePath, _ := cmd.Flags().GetString("profile")
	if profilePath == "" {
		prompt := promptui.Prompt{Label: "Path to .mobileprovision file"}
		profilePath, err = prompt.Run()
		if err != nil {
			return err
		}
	}

	// Validate profile file
	profileData, err := os.ReadFile(profilePath)
	if err != nil {
		return fmt.Errorf("failed to read provisioning profile: %w", err)
	}
	fmt.Printf("Profile: %s (%.1f KB)\n", profilePath, float64(len(profileData))/1024)

	// Get certificate password (no echo)
	passwordPrompt := promptui.Prompt{
		Label: "Certificate password",
		Mask:  '*',
	}
	password, err := passwordPrompt.Run()
	if err != nil {
		return fmt.Errorf("failed to read password: %w", err)
	}
	if password == "" {
		return fmt.Errorf("certificate password is required")
	}

	fmt.Println()
	fmt.Printf("Uploading secrets to %s/%s...\n", cfg.GitHub.Owner, cfg.GitHub.Repo)

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Get repository public key for encryption
	publicKey, err := ghClient.GetPublicKey(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo)
	if err != nil {
		return fmt.Errorf("failed to get repository public key: %w", err)
	}

	// Base64 encode the files
	certBase64 := base64.StdEncoding.EncodeToString(certData)
	profileBase64 := base64.StdEncoding.EncodeToString(profileData)

	// Encrypt and upload secrets
	secrets := map[string]string{
		"IOS_CERTIFICATE":            certBase64,
		"IOS_CERTIFICATE_PASSWORD":   password,
		"IOS_PROVISIONING_PROFILE":   profileBase64,
	}

	for name, value := range secrets {
		encrypted, err := github.EncryptSecret(publicKey.Key, value)
		if err != nil {
			return fmt.Errorf("failed to encrypt %s: %w", name, err)
		}

		if err := ghClient.CreateOrUpdateSecret(ctx, cfg.GitHub.Owner, cfg.GitHub.Repo, name, encrypted, publicKey.KeyID); err != nil {
			return fmt.Errorf("failed to upload %s: %w", name, err)
		}
		fmt.Printf("  Uploaded: %s\n", name)
	}

	// Update config to indicate signing is enabled
	cfg.IOS.Signing = true
	mgr := config.NewManager()
	if err := mgr.Save(cfg); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}
	fmt.Println("  Updated: builder.json (signing enabled)")

	fmt.Println()
	fmt.Println("Code signing configured successfully!")
	fmt.Println()
	fmt.Println("Your next build will be signed. To build unsigned, use:")
	fmt.Println("  builder ios build --unsigned")

	return nil
}
