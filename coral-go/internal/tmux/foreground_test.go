package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// shellWait bounds how long a pane may take to show a command as the foreground job or the shell as idle again.
// Starting the command and ps/pgrep each take a moment; 5 s was too tight for a machine under load (it failed in a
// 188 s full run), and a pass returns as soon as the state is seen, so a long bound costs nothing.
const shellWait = 20 * time.Second

func TestIsShellCommand(t *testing.T) {
	for _, c := range []string{"zsh", "-zsh", "bash", "sh", "/bin/zsh", "fish", "dash", "pwsh", "pwsh.exe", "cmd.exe", "ZSH", " bash "} {
		assert.True(t, IsShellCommand(c), c)
	}
	for _, c := range []string{"node", "claude", "codex", "gemini", "python3", "sleep", "vim", "", "zshrc", "bash-language-server"} {
		assert.False(t, IsShellCommand(c), c)
	}
}

// #3219: the agent CLI is typed into the pane's shell at launch. While it (or a
// wrapper script running it) runs, the shell has a child and is not idle; once it
// exits, the shell is idle at its prompt.
func TestAgentShellIdleFollowsTheCLI(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const sid = "3a9e1c52-7b4d-4f0e-9c6a-2d8b1e5f4a70"
	name := "claude-" + sid
	createTestSession(t, c, name, t.TempDir())
	idle := func() bool {
		v, _, err := c.AgentShellIdle(ctx, name, "claude", sid)
		return err == nil && v
	}
	busy := func() bool {
		v, _, err := c.AgentShellIdle(ctx, name, "claude", sid)
		return err == nil && !v
	}

	require.Eventually(t, idle, shellWait, 25*time.Millisecond, "a fresh pane is at an idle shell prompt")

	// A long-running command stands in for the agent CLI.
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, name+".0", "sleep 30"))
	require.Eventually(t, busy, shellWait, 25*time.Millisecond, "a running CLI keeps the shell busy")
	require.NoError(t, c.SendRawKeys(ctx, terminalinput.Unclassified, name, []string{"C-c"}, "claude", sid))
	require.Eventually(t, idle, shellWait, 25*time.Millisecond, "idle again once it exits")

	// A CLI started through a shell wrapper script shows 'sh' in the foreground, yet
	// it is running: the pane's shell has a child, so it is not taken as exited.
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, name+".0", "sh -c 'sleep 30; true'"))
	require.Eventually(t, func() bool {
		fg, err := c.DisplayMessage(ctx, name+".0", "#{pane_current_command}")
		return err == nil && fg != "" && busy()
	}, shellWait, 25*time.Millisecond, "a wrapper script is not an exited agent")
	require.NoError(t, c.SendRawKeys(ctx, terminalinput.Unclassified, name, []string{"C-c"}, "claude", sid))
	require.Eventually(t, idle, shellWait, 25*time.Millisecond)
}

func TestAgentShellIdleOfAGoneTerminal(t *testing.T) {
	c := newTestClient(t)
	_, _, err := c.AgentShellIdle(context.Background(), "claude-0b1c2d3e-4f50-4617-8293-a4b5c6d7e8f9", "claude", "0b1c2d3e-4f50-4617-8293-a4b5c6d7e8f9")
	assert.True(t, errors.Is(err, ErrTerminalGone), "err = %v", err)
}

// #3268: a background job left in the shell after the agent CLI exits ('sleep &') is
// a child of the shell, but nothing is running in the foreground: the shell is still
// at its prompt, so the agent has exited and nothing may be typed into it.
func TestAgentShellIdleIgnoresBackgroundJobs(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const sid = "5c7e9a21-3b4d-4f60-8a1b-9d2e3f4a5b60"
	name := "claude-" + sid
	createTestSession(t, c, name, t.TempDir())
	state := func() (bool, bool) {
		v, _, err := c.AgentShellIdle(ctx, name, "claude", sid)
		return v, err == nil
	}
	require.Eventually(t, func() bool { v, ok := state(); return ok && v }, shellWait, 25*time.Millisecond)

	// A job in the background: the shell is back at its prompt.
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, name+".0", "sleep 60 &"))
	// The job exists: a sleep under the pane's shell. The pane's pid alone is there from the
	// start, so it said nothing about the job (#3788).
	pid, err := c.DisplayMessage(ctx, name+".0", "#{pane_pid}")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		out, _ := exec.Command("pgrep", "-P", strings.TrimSpace(pid), "sleep").Output()
		return strings.TrimSpace(string(out)) != ""
	}, shellWait, 25*time.Millisecond, "the background sleep never started")
	time.Sleep(500 * time.Millisecond) // sleep-ok: a negative proof; the shell must still read as idle after the job has had time to be wrongly counted
	idle, ok := state()
	require.True(t, ok)
	assert.True(t, idle, "a background job is not the agent: the shell is idle")

	// A foreground command on top of the background job is still busy.
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, name+".0", "sleep 30"))
	require.Eventually(t, func() bool { v, ok := state(); return ok && !v }, shellWait, 25*time.Millisecond, "a foreground command keeps the shell busy")
	require.NoError(t, c.SendRawKeys(ctx, terminalinput.Unclassified, name, []string{"C-c"}, "claude", sid))
	require.Eventually(t, func() bool { v, ok := state(); return ok && v }, shellWait, 25*time.Millisecond)
}

// #3728: the process table is read with one ps run; the command is the rest of the line, spaces
// included (a macOS command is a path).
func TestParseProcTable(t *testing.T) {
	out := "    1     0 Ss   /sbin/launchd\n" +
		"  500     1 S    /Applications/Some App.app/Contents/MacOS/Some App\n" +
		"  600   500 Ss   -zsh\n" +
		"  601   600 S+   claude\n" +
		"  602   600 S    sleep\n" +
		"garbage line\n"
	tb := parseProcTable(out)
	assert.Equal(t, "/Applications/Some App.app/Contents/MacOS/Some App", tb.byPID[500].comm)
	assert.Equal(t, procEntry{ppid: 600, stat: "S+", comm: "claude"}, tb.byPID[601])
	assert.ElementsMatch(t, []int{601, 602}, tb.children[600])
	assert.Len(t, tb.byPID, 5)
}

// #3728: the checks of many agents made together share one ps run.
func TestShellIdleSharesOneProcessSnapshot(t *testing.T) {
	skipWithoutUnixTmux(t)
	procTableMu.Lock()
	procTableLast = nil
	procTableMu.Unlock()
	before := procTableRuns.Load()
	for i := 0; i < 20; i++ {
		_, _, err := ShellIdle(os.Getpid())
		require.NoError(t, err)
	}
	assert.Equal(t, int64(1), procTableRuns.Load()-before, "one ps run for twenty checks")
	_, _, err := ShellIdle(1 << 30)
	assert.ErrorContains(t, err, "no such process")
}

// #5171, #5170: a shell started since the shared process snapshot (a restart's new terminal) is looked up again, not reported as unknown (which
// counted as a running agent and let automated text be typed into a bare shell).
func TestShellIdleLooksAgainForAShellThatStartedAfterTheSnapshot(t *testing.T) {
	skipWithoutUnixTmux(t)
	procTableMu.Lock()
	procTableLast = &procTable{byPID: map[int]procEntry{}, children: map[int][]int{}, at: time.Now().Add(-500 * time.Millisecond)}
	procTableMu.Unlock()
	_, _, err := ShellIdle(os.Getpid())
	require.NoError(t, err, "the process is real: a snapshot without it is stale")
	_, _, err = ShellIdle(1 << 30)
	assert.ErrorContains(t, err, "no such process", "a pid that does not exist still does not")
}

// A pid that is not there (a pane that is gone) makes one fresh process-table read a second, not one per check.
func TestShellIdleFreshLookIsLimitedPerPidPerSecond(t *testing.T) {
	skipWithoutUnixTmux(t)
	const gone = 1<<30 + 12345
	freshLookMu.Lock()
	delete(freshLooks, gone)
	freshLookMu.Unlock()
	before := procTableRuns.Load()
	for i := 0; i < 5; i++ {
		procTableMu.Lock()
		procTableLast = &procTable{byPID: map[int]procEntry{}, children: map[int][]int{}, at: time.Now().Add(-500 * time.Millisecond)} // stale each time
		procTableMu.Unlock()
		_, _, err := ShellIdle(gone)
		require.ErrorContains(t, err, "no such process")
	}
	assert.Equal(t, int64(1), procTableRuns.Load()-before, "one fresh ps run for five checks of a pid that is not there")
}
