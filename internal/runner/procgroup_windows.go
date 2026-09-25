//go:build windows

package runner

import "os/exec"

// The central runner executes on macOS. Windows builds of this package keep
// exec's default cancellation, which stops only the script process itself.
func runInOwnProcessGroup(*exec.Cmd) {}

func killProcessGroup(*exec.Cmd) {}
