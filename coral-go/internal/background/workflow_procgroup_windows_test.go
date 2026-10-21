//go:build windows

package background

import (
	"context"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// processGone reports whether the process pid has exited (or can't be opened any more).
func processGone(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, 0)
	return ev == windows.WAIT_OBJECT_0
}

// stepGroupsGone: every step process a workflow test started has exited (on Windows the
// step's job takes its children with it, #3997).
func stepGroupsGone(t *testing.T, pids []int) {
	t.Helper()
	for _, p := range pids {
		assert.Eventually(t, func() bool { return processGone(p) }, 10*time.Second, 20*time.Millisecond,
			"the step's process %d is still running", p)
	}
}

// jobAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type jobAccounting struct {
	TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
}

func activeInJob(t *testing.T, job windows.Handle) uint32 {
	var info jobAccounting
	err := windows.QueryInformationJobObject(job, 1 /* JobObjectBasicAccountingInformation */, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	require.NoError(t, err)
	return info.ActiveProcesses
}

// #3997: a killed shell step's job ends with everything in it: sh and the sleep it started.
// Before, only sh was killed.
func TestKillChildProcessEndsTheStepsJob(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "sleep 600 & wait") // as the steps make it
	configureChildProcess(cmd)
	require.NoError(t, cmd.Start())
	trackChildProcess(cmd)
	v, ok := stepJobs.Load(cmd)
	require.True(t, ok, "the step joined a job")
	var job windows.Handle
	self := windows.CurrentProcess()
	require.NoError(t, windows.DuplicateHandle(self, v.(windows.Handle), self, &job, 0, false, windows.DUPLICATE_SAME_ACCESS))
	defer windows.CloseHandle(job)
	require.Eventually(t, func() bool { return activeInJob(t, job) >= 2 }, 10*time.Second, 20*time.Millisecond, "sh and its sleep")
	killChildProcess(cmd)
	_ = cmd.Wait()
	assert.Eventually(t, func() bool { return activeInJob(t, job) == 0 }, 10*time.Second, 20*time.Millisecond, "nothing in the job survived")
}

// A step starts suspended and joins its job before it runs: a grandchild it spawns at once is in the
// job, so killing the step ends it too (a child started in the old gap escaped the kill).
func TestQuicklySpawnedGrandchildDiesWithTheStep(t *testing.T) {
	shell, err := workflowShell()
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		cmd := exec.CommandContext(context.Background(), shell, "-c", "sleep 600 & sleep 600 & wait")
		configureChildProcess(cmd)
		require.NoError(t, cmd.Start())
		trackChildProcess(cmd)
		v, ok := stepJobs.Load(cmd)
		require.True(t, ok)
		var job windows.Handle
		self := windows.CurrentProcess()
		require.NoError(t, windows.DuplicateHandle(self, v.(windows.Handle), self, &job, 0, false, windows.DUPLICATE_SAME_ACCESS))
		require.Eventually(t, func() bool { return activeInJob(t, job) >= 3 }, 10*time.Second, 10*time.Millisecond, "sh and both sleeps are in the job")
		killChildProcess(cmd)
		_ = cmd.Wait()
		assert.Eventually(t, func() bool { return activeInJob(t, job) == 0 }, 10*time.Second, 20*time.Millisecond, "nothing survived")
		windows.CloseHandle(job)
	}
}

func TestStepProcessRunsAfterTrackingStartedItSuspended(t *testing.T) {
	shell, err := workflowShell()
	require.NoError(t, err)
	cmd := exec.CommandContext(context.Background(), shell, "-c", "exit 7")
	configureChildProcess(cmd)
	require.NoError(t, cmd.Start())
	trackChildProcess(cmd)
	var ee *exec.ExitError
	require.ErrorAs(t, cmd.Wait(), &ee)
	assert.Equal(t, 7, ee.ExitCode())
	releaseChildProcess(cmd)
}
