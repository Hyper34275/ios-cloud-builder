//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

// processTree is everything a test script starts. On Unix it is a process
// group led by the script, so a timeout, and the cleanup after the script
// exits, reach every process it started rather than only the interpreter.
type processTree struct {
	cmd     *exec.Cmd
	stopped bool
}

// newProcessTree prepares cmd, before it starts, to lead a new process group.
// A timeout sends SIGTERM to the whole group; cmd.WaitDelay later escalates to
// killing the script itself.
func newProcessTree(cmd *exec.Cmd) (*processTree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return signalProcessGroup(cmd, syscall.SIGTERM) }
	return &processTree{cmd: cmd}, nil
}

// started is called right after cmd.Start. Setpgid already placed the script
// in its group atomically, so there is nothing left to do.
func (*processTree) started() error { return nil }

// stop kills anything the script left running in the background, so nothing
// it started keeps writing once its output has been encrypted. Only the first
// call signals, so a group ID can never be signalled again once it might have
// been reused.
func (tree *processTree) stop() {
	if tree.stopped {
		return
	}
	tree.stopped = true
	_ = signalProcessGroup(tree.cmd, syscall.SIGKILL)
}

func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, signal)
}
