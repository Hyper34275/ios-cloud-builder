package main

import (
	"context"
	"fmt"
	"time"

	"github.com/MobAI-App/ios-builder/internal/snapshot"
	"github.com/spf13/cobra"
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove abandoned private snapshot refs",
	RunE:  runCleanup,
}

func init() {
	rootCmd.AddCommand(cleanupCmd)
	cleanupCmd.Flags().StringP("remote", "r", "origin", "Private source git remote")
	cleanupCmd.Flags().Duration("older-than", snapshot.DefaultMaxAge, "Delete snapshots at least this old")
}

func runCleanup(cmd *cobra.Command, _ []string) error {
	remote, _ := cmd.Flags().GetString("remote")
	maxAge, _ := cmd.Flags().GetDuration("older-than")
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := snapshot.VerifyRemote(ctx, remote, cfg.GitHub.Owner, cfg.GitHub.Repo); err != nil {
		return fmt.Errorf("verify configured source remote: %w", err)
	}
	removed, err := snapshot.Cleanup(ctx, remote, maxAge, time.Now())
	if err != nil {
		return fmt.Errorf("cleanup stale snapshot refs: %w", err)
	}
	if len(removed) == 0 {
		fmt.Println("No stale snapshot refs found.")
		return nil
	}
	for _, ref := range removed {
		fmt.Printf("Removed %s (created %s)\n", ref.Ref, ref.CreatedAt.Format(time.RFC3339))
	}
	return nil
}
