//go:build windows

package runner

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobDrainTimeout bounds how long stop waits for terminated processes to be
// torn down. Until they are, their handles keep the private log open, and
// Windows refuses to replace or delete an open file.
const jobDrainTimeout = 30 * time.Second

// processTree is everything a test script starts. On Windows it is a job
// object: the script is created suspended, assigned to the job, and only then
// resumed, so every process it starts, at any depth, is born inside the job.
// That includes the long-lived helpers build tools leave behind, such as
// MSBuild node-reuse workers, the Roslyn compiler server (VBCSCompiler), and
// dotnet build servers. Terminating the job ends all of them at once. The job
// is also KILL_ON_JOB_CLOSE, so the tree dies with the runner if the runner
// itself is killed.
type processTree struct {
	cmd *exec.Cmd
	job windows.Handle
}

func newProcessTree(cmd *exec.Cmd) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// No JOB_OBJECT_LIMIT_BREAKAWAY_OK: a child cannot leave the job with
	// CREATE_BREAKAWAY_FROM_JOB.
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure job object: %w", err)
	}
	tree := &processTree{cmd: cmd, job: job}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	// A timeout ends the whole tree. Windows has no SIGTERM for console
	// programs that is reliable without a console, so there is no grace period.
	cmd.Cancel = tree.terminate
	return tree, nil
}

// started assigns the suspended script to the job and resumes it. On error
// the caller kills the still-suspended script.
func (tree *processTree) started() error {
	pid := uint32(tree.cmd.Process.Pid)
	// cmd.Process holds a handle to the process, so this PID cannot have been
	// reused by another process in the meantime.
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("open test script process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err := windows.AssignProcessToJobObject(tree.job, process); err != nil {
		return fmt.Errorf("assign test script to job object: %w", err)
	}
	return resumeProcess(pid)
}

// resumeProcess resumes the threads of a process created with
// CREATE_SUSPENDED. os/exec closes the primary thread handle, so the thread is
// found again by enumerating the system's threads.
func resumeProcess(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot threads: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return fmt.Errorf("open test script thread: %w", openErr)
		}
		_, resumeErr := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if resumeErr != nil {
			return fmt.Errorf("resume test script: %w", resumeErr)
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("enumerate threads: %w", err)
	}
	if resumed == 0 {
		return errors.New("test script has no thread to resume")
	}
	return nil
}

func (tree *processTree) terminate() error {
	if tree.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(tree.job, 1)
}

// stop terminates every process still in the job, waits until Windows has
// torn them down so none of them holds the private log open, and closes the
// job.
func (tree *processTree) stop() {
	if tree.job == 0 {
		return
	}
	_ = tree.terminate()
	deadline := time.Now().Add(jobDrainTimeout)
	for time.Now().Before(deadline) {
		active, err := activeJobProcesses(tree.job)
		if err != nil || active == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = windows.CloseHandle(tree.job)
	tree.job = 0
}

// jobBasicAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type jobBasicAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func activeJobProcesses(job windows.Handle) (uint32, error) {
	var info jobBasicAccounting
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return 0, err
	}
	return info.ActiveProcesses, nil
}
