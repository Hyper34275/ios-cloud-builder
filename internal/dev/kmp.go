package dev

import (
	"context"
	"fmt"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/mobai"
)

// KMPHandler implements FrameworkHandler for Kotlin Multiplatform projects.
//
// Kotlin Multiplatform has no hot reload on iOS: the shared Kotlin code is
// compiled to a native framework at build time, so there is no runtime to swap
// code into. This handler therefore only installs and launches the app; for
// code changes the IPA must be rebuilt with 'builder ios build'.
type KMPHandler struct {
	showLogs bool
}

// NewKMPHandler creates a new Kotlin Multiplatform handler.
func NewKMPHandler(showLogs bool) *KMPHandler {
	return &KMPHandler{showLogs: showLogs}
}

// Setup is a no-op for Kotlin Multiplatform.
func (h *KMPHandler) Setup(ctx context.Context) error { return nil }

// DebugConfig returns nil; Kotlin Multiplatform needs no special launch config.
func (h *KMPHandler) DebugConfig() *mobai.DebugConfig { return nil }

// Attach keeps the app running and streams output. There is no hot reload.
func (h *KMPHandler) Attach(ctx context.Context, deviceID string, debugOutput <-chan mobai.DebugOutput) error {
	fmt.Println()
	fmt.Println("App launched on device.")
	fmt.Println("Kotlin Multiplatform has no hot reload on iOS.")
	fmt.Println("For code changes, rebuild with 'builder ios build' and re-run this command.")
	fmt.Println("  - Press Ctrl+C to stop")
	fmt.Println()

	for output := range debugOutput {
		switch output.Type {
		case "exit":
			fmt.Println("App exited")
			return nil
		case "error":
			if strings.Contains(output.Message, "launch failed") {
				fmt.Println()
				fmt.Println("Launch failed: developer not trusted.")
				fmt.Println()
				fmt.Println("On device go to: Settings > General > VPN & Device Management")
				fmt.Println("Then tap your developer certificate and trust it.")
				return fmt.Errorf("launch failed: developer not trusted")
			}
			fmt.Printf("App error: %s\n", output.Message)
		case "stdout", "stderr":
			if h.showLogs && output.Data != "" {
				fmt.Print(output.Data)
			}
		}
	}

	return nil
}

// Stop is a no-op for Kotlin Multiplatform.
func (h *KMPHandler) Stop() {}
