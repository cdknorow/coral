//go:build windows

package ptymanager

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// newKillJob puts the process the terminal started into a job object that ends every process in it when the job is
// terminated or its handle closes, so a stop ends the whole tree (the shell, the agent and what the agent started), not only
// the process ConPTY was given (#68). The assignment is made right after the start, before the shell has started children.
// A failure gives 0: the stop then ends the pseudo console as before.
func newKillJob(pid int) windows.Handle {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return 0
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		windows.CloseHandle(job)
		return 0
	}
	return job
}

// hasChildProcesses says whether any process has parent pid (a snapshot of the process list).
func hasChildProcesses(pid int) (bool, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return false, err
	}
	for {
		if int(e.ParentProcessID) == pid {
			return true, nil
		}
		if err := windows.Process32Next(snap, &e); err != nil {
			return false, nil
		}
	}
}
