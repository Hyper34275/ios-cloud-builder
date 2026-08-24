package main

import (
	"fmt"

	"github.com/MobAI-App/ios-builder/internal/security"
	"github.com/spf13/cobra"
)

var securityCmd = &cobra.Command{
	Use:   "security",
	Short: "Manage local artifact-encryption identity",
}

var securityInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create or reuse the local AGE identity",
	RunE: func(_ *cobra.Command, _ []string) error {
		recipient, err := security.EnsureIdentity()
		if err != nil {
			return err
		}
		fmt.Println("Local AGE identity is ready.")
		fmt.Println("Public recipient:", recipient)
		fmt.Println("The private identity remains local and was not written to builder.json or GitHub.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(securityCmd)
	securityCmd.AddCommand(securityInitCmd)
}
