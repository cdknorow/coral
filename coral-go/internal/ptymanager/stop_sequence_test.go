package ptymanager

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/inputlog"
)

type stopRecorder struct {
	mu sync.Mutex
	w  [][]byte
}

func (r *stopRecorder) write(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.w = append(r.w, append([]byte(nil), b...))
	return nil
}

func (r *stopRecorder) writes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, b := range r.w {
		out = append(out, string(b))
	}
	return out
}

func yesShell() bool { return true }
func noShell() bool  { return false }

var fastStop = stopTimings{secondAfter: 20 * time.Millisecond, poll: 5 * time.Millisecond, limit: 300 * time.Millisecond}

// #68 (audit F12): a stop that is one Ctrl-C leaves a raw-mode CLI waiting out the whole grace. The second Ctrl-C follows,
// and "exit" only once nothing is running under the shell.
func TestPoliteStopSendsASecondCtrlCThenExitForAnIdleShell(t *testing.T) {
	var r stopRecorder
	done := make(chan struct{})
	politeStop(r.write, done, func() (bool, error) { return false, nil }, yesShell, fastStop)
	assert.Equal(t, []string{"\x03", "exit\r"}, r.writes())
}

func TestPoliteStopNeverTypesExitWhileAProgramIsRunning(t *testing.T) {
	var r stopRecorder
	done := make(chan struct{})
	start := time.Now()
	politeStop(r.write, done, func() (bool, error) { return true, nil }, yesShell, fastStop)
	assert.Equal(t, []string{"\x03"}, r.writes(), "exit would be typed into the agent's input")
	assert.GreaterOrEqual(t, time.Since(start), fastStop.limit, "it waited out its limit")
	// A process list that can't be read is not an idle shell either.
	var r2 stopRecorder
	politeStop(r2.write, done, func() (bool, error) { return false, errors.New("snapshot") }, yesShell, fastStop)
	assert.Equal(t, []string{"\x03"}, r2.writes())
}

func TestPoliteStopStopsWhenTheProcessIsGone(t *testing.T) {
	var r stopRecorder
	done := make(chan struct{})
	close(done) // exited on the first Ctrl-C
	politeStop(r.write, done, func() (bool, error) { return false, nil }, yesShell, fastStop)
	assert.Empty(t, r.writes(), "no second Ctrl-C for a process that already went")

	// Gone after the second.
	var r2 stopRecorder
	done2 := make(chan struct{})
	go func() { time.Sleep(fastStop.secondAfter + 10*time.Millisecond); close(done2) }()
	politeStop(r2.write, done2, func() (bool, error) { return true, nil }, yesShell, fastStop)
	assert.Equal(t, []string{"\x03"}, r2.writes())
}

// #5665: an agent started directly as the terminal's root has no child of its own, so "no child" does not mean "an idle shell": "exit" would be a
// message typed into the agent. It is typed only when the root is a known shell; an unknown root is not one.
func TestPoliteStopNeverTypesExitIntoAnAgentStartedAsTheRoot(t *testing.T) {
	var r stopRecorder
	done := make(chan struct{})
	start := time.Now()
	politeStop(r.write, done, func() (bool, error) { return false, nil }, noShell, fastStop) // no child, and the root is not a shell
	assert.Equal(t, []string{"\x03"}, r.writes(), "the second Ctrl-C, and nothing typed into the agent")
	assert.GreaterOrEqual(t, time.Since(start), fastStop.limit, "it waited out its limit and left the rest to the force kill")

	// A shell with nothing running still gets "exit".
	var r2 stopRecorder
	politeStop(r2.write, done, func() (bool, error) { return false, nil }, yesShell, fastStop)
	assert.Equal(t, []string{"\x03", "exit\r"}, r2.writes())
}

func TestIsShellImageKnowsTheWindowsShellsAndNothingElse(t *testing.T) {
	for _, shell := range []string{"powershell.exe", "PowerShell.EXE", `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "pwsh.exe", `C:\Program Files\PowerShell\7\pwsh.exe`, "cmd.exe", `C:\Windows\System32\CMD.EXE`, "C:/Windows/System32/cmd.exe"} {
		assert.True(t, isShellImage(shell), shell)
	}
	for _, not := range []string{"", "claude.exe", "codex.exe", "node.exe", `C:\Users\x\claude.exe`, "powershell.exe.bak", "mycmd.exe", "cmd.exe.exe", "powershell", "cmd", `C:\tools\powershell.exe\claude.exe`, "pwsh"} {
		assert.False(t, isShellImage(not), not)
	}
}

// #5706 (Security's answer a): the second Ctrl-C and the exit line of a polite stop are recorded in the input debug log, as Coral's own
// control input, and are written to the console exactly as before.
func TestPoliteStopRecordsItsWritesAsInternalNotes(t *testing.T) {
	dir := t.TempDir()
	inputlog.Setup(dir, true)
	t.Cleanup(func() { inputlog.Setup("", false); inputlog.SetSetting(false) })
	var r stopRecorder
	politeStop(noteStopWrite("claude-stop", r.write), make(chan struct{}), func() (bool, error) { return false, nil }, yesShell, fastStop)
	assert.Equal(t, []string{"\x03", "exit\r"}, r.writes(), "the console gets what it got before")
	inputlog.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, inputlog.FileName))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `target=claude-stop `)
	assert.Contains(t, string(raw), `kind=keys keys="C-c"`)
	assert.Contains(t, string(raw), `kind=text len=4 text="exit"`)
	assert.Contains(t, string(raw), `kind=keys keys="Enter"`)
}
