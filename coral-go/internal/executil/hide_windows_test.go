//go:build windows

package executil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Coral server started with no console (by a scheduled task, a launcher, or any DETACHED_PROCESS parent) gives every console program it starts a console WINDOW of its
// own, unless it starts that program with CREATE_NO_WINDOW (#211). The test starts a Coral-like parent with DETACHED_PROCESS; the parent runs a probe through the helper;
// the probe reports whether its console has a window (GetConsoleWindow) and its output must come back through the pipe.

const (
	hideModeEnv     = "CORAL_HIDE_TEST_MODE"
	detachedProcess = 0x00000008
)

var procGetConsoleWindow = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")

// TestHideHelperProcess is not a test: it is the body of the parent and the probe, run by the tests below in a process of their own.
func TestHideHelperProcess(t *testing.T) {
	switch os.Getenv(hideModeEnv) {
	case "probe": // what a CLI version probe is to Coral: a console program whose output the server reads
		hwnd, _, _ := procGetConsoleWindow.Call()
		fmt.Printf("probe-output window=%v\n", hwnd != 0)
		os.Exit(0)
	case "parent-hidden", "parent-plain": // the Coral-like server, with no console
		cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHideHelperProcess$")
		cmd.Env = append(os.Environ(), hideModeEnv+"=probe")
		if os.Getenv(hideModeEnv) == "parent-hidden" {
			HideWindow(cmd)
		}
		out, err := cmd.Output()
		if err != nil {
			fmt.Printf("probe-error %v\n", err)
			os.Exit(3)
		}
		fmt.Print(string(out))
		os.Exit(0)
	}
}

// runDetachedParent starts the Coral-like parent with DETACHED_PROCESS and returns the probe's report.
func runDetachedParent(t *testing.T, mode string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHideHelperProcess$")
	cmd.Env = append(os.Environ(), hideModeEnv+"="+mode)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess}
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.Output(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the parent did not finish")
	}
	require.NoError(t, err, string(out))
	return string(out)
}

func TestAProbeStartedByAConsoleLessParentOpensNoWindowThroughHideWindow(t *testing.T) {
	out := runDetachedParent(t, "parent-hidden")
	assert.Contains(t, out, "probe-output window=false", "the probe's output comes back through the pipe, and its console has no window")
	assert.False(t, strings.Contains(out, "window=true"), out)
}

// The control (opt-in, it flashes a window): the same probe without HideWindow. Where Windows gives it a console window the check above is proved to bite; where it does not (a machine whose
// console host draws no window for it), the control says so instead of failing.
func TestTheSameProbeWithoutHideWindowGetsAWindow(t *testing.T) {
	if os.Getenv("CORAL_TEST_WINDOW_CONTROL") != "1" {
		t.Skip("this control opens a real console window for a moment: set CORAL_TEST_WINDOW_CONTROL=1 to run it")
	}
	out := runDetachedParent(t, "parent-plain")
	if !strings.Contains(out, "window=true") {
		t.Skipf("on this machine a probe of a console-less parent has no console window even without HideWindow (%q): the test above cannot show the difference here", strings.TrimSpace(out))
	}
}

func TestCommandHidesTheWindowToo(t *testing.T) {
	cmd := Command(context.Background(), os.Args[0], "-test.run=^$")
	require.NotNil(t, cmd.SysProcAttr)
	assert.NotZero(t, cmd.SysProcAttr.CreationFlags&0x08000000, "executil.Command is CREATE_NO_WINDOW")
}
