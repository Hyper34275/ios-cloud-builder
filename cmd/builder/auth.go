package main

import (
	"context"
	"fmt"

	"github.com/MobAI-App/ios-builder/internal/auth"
	"github.com/spf13/cobra"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authentication commands",
}

var authGitHubCmd = &cobra.Command{
	Use:   "github",
	Short: "Authenticate with GitHub",
	Long:  `Authenticates with GitHub using OAuth Device Flow and stores the token securely in your system keychain.`,
	RunE:  runAuthGitHub,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored credentials",
	RunE:  runAuthLogout,
}

func init() {
	authCmd.AddCommand(authGitHubCmd)
	authCmd.AddCommand(authLogoutCmd)
}

func runAuthGitHub(cmd *cobra.Command, args []string) error {
	fmt.Println("Authenticating with GitHub...")

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	token, err := auth.Login(ctx)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	fmt.Println()
	fmt.Printf("Authenticated successfully (scope: %s)\n", token.Scope)
	return nil
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	if err := auth.Logout(); err != nil {
		return err
	}
	fmt.Println("Logged out successfully")
	return nil
}
