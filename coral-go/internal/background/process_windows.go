//go:build windows

package background

import (
	"context"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no POSIX process groups: a shell step joins a job object right after it starts
// (#3997), so a kill (cancel, timeout, KillRun, Coral stopping) ends sh and everything it
// started, and the job dies with Coral (KILL_ON_JOB_CLOSE on the handle Coral holds). Before,
// only sh was killed and its children kept running.

// stepJobs maps a started step to its job.
var stepJobs sync.Map // *exec.Cmd -> windows.Handle

func configureChildProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		killChildProcess(cmd)
		return nil
	}
}

func setJobKillOnClose(job windows.Handle, on bool) error {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if on {
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	}
	_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return err
}

// trackChildProcess puts the started cmd in a new job. A failure leaves it unjobbed (the kill
// then reaches sh only, as before).
func trackChildProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	if setJobKillOnClose(job, true) != nil {
		windows.CloseHandle(job)
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	err = windows.AssignProcessToJobObject(job, h)
	windows.CloseHandle(h)
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	stepJobs.Store(cmd, job)
}

// releaseChildProcess runs after the step ended by itself: what it left running keeps running,
// as on Unix, so the job's kill-on-close is cleared before its handle is closed.
func releaseChildProcess(cmd *exec.Cmd) {
	if v, ok := stepJobs.LoadAndDelete(cmd); ok {
		job := v.(windows.Handle)
		_ = setJobKillOnClose(job, false)
		windows.CloseHandle(job)
	}
}

// waitChildKills: a job object ends the step whole, so there is no escalation to wait for.
func waitChildKills(context.Context) {}

// killChildProcess ends the step's job (sh and everything it started), else sh.
func killChildProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if v, ok := stepJobs.LoadAndDelete(cmd); ok {
		job := v.(windows.Handle)
		_ = windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		return
	}
	_ = cmd.Process.Kill()
}
