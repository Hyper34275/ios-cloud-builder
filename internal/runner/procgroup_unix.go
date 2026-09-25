//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

// runInOwnProcessGroup makes cmd the leader of a new process group, so a
// timeout, and the cleanup after the script exits, reach every process it
// started rather than only bash itself.
func runInOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return signalProcessGroup(cmd, syscall.SIGTERM) }
}

// killProcessGroup stops anything the script left running in the background,
// so nothing it started keeps writing once its output has been encrypted.
func killProcessGroup(cmd *exec.Cmd) {
	_ = signalProcessGroup(cmd, syscall.SIGKILL)
}

func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, signal)
}
