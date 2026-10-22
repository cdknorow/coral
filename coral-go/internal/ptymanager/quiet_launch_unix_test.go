//go:build !windows

package ptymanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

func TestRestartRingAtClearKeepsFromTheClearOn(t *testing.T) {
	seq := string(clearScrollback)
	cases := []struct {
		name, ring string
		n          int
		want       string
	}{
		{"no clear: as it was", "line one\nline two\n", 9, "line one\nline two\n"},
		{"a clear in the new bytes", "typed line\n" + seq + "agent", len(seq) + len("agent"), seq + "agent"},
		{"the last of two clears", "a" + seq + "b" + seq + "c", len(seq) + 1, seq + "c"},
		{"a clear in old bytes only: not looked for again", "a" + seq + "b", 1, "a" + seq + "b"},
		{"a clear straddling two reads", "before\x1b[3", 3 + 2, "before\x1b[3"},
	}
	for _, tc := range cases {
		out, ok := restartRingAtClear(append([]byte(nil), tc.ring...), tc.n)
		assert.Equal(t, tc.want, string(out), tc.name)
		assert.Equal(t, strings.Contains(tc.ring[max(0, len(tc.ring)-tc.n-len(seq)+1):], seq), ok, tc.name)
	}
	// a read that completes a sequence begun in the one before
	ring := []byte("before\x1b[3")
	ring = append(ring, 'J', 'x')
	out, ok := restartRingAtClear(ring, 2)
	assert.True(t, ok)
	assert.Equal(t, seq+"x", string(out), "straddling")
}

// #5911 on the PTY backend: the launch line is a short line sourcing a private script; the replay holds the agent's output and none of the line, the script's contents or the
// token's value; the script removed itself (or stays with CORAL_KEEP_LAUNCH_SCRIPTS); a token's file is read and removed by the shell as before.
func TestPTYQuietLaunchStartsTheAgentWithoutItsLineOnScreen(t *testing.T) {
	skipIfWindows(t)
	canFork(t)
	dir, err := os.MkdirTemp("", "q5911")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	t.Setenv("CORAL_KEEP_LAUNCH_SCRIPTS", "")
	terminal, backend := newTestTerminal(t)
	spawnTestSession(t, backend, "quiet-pty")
	tokenFile := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("s3cr3t-token-value\n"), 0o600))
	line := "export CORAL_QUIET_MARKER_LONG_NAME_0123456789=value-0123456789; " +
		`export CORAL_SESSION_TOKEN="$(cat '` + tokenFile + `')"; rm -f '` + tokenFile + `'; ` +
		`test "$CORAL_SESSION_TOKEN" = "s3cr3t-token-value" && echo token-read-$((6*7))`
	require.NoError(t, terminal.SendQuietLaunchToTarget(context.Background(), terminalinput.Internal, "quiet-pty", line))

	replay := waitForPTYOutput(t, backend, "quiet-pty", "token-read-42", 10*time.Second)
	require.Contains(t, replay, "token-read-42", "the line ran, with the token read from its file")
	assert.NotContains(t, replay, "CORAL_QUIET_MARKER_LONG_NAME", "the exports are not on the screen")
	assert.NotContains(t, replay, "s3cr3t-token-value", "the token's value is nowhere")
	assert.NotContains(t, replay, "coral-command-", "nor the line that sourced the script")
	assert.Contains(t, replay, "\x1b[3J", "the replay restarts at the clear")
	assert.False(t, strings.Contains(replay[:strings.Index(replay, "\x1b[3J")], "coral-command"), "nothing before the clear")
	_, err = os.Stat(tokenFile)
	assert.True(t, os.IsNotExist(err), "the shell removed the token file")
	gone, _ := filepath.Glob(filepath.Join(dir, "coral-command-*.sh"))
	assert.Empty(t, gone, "the script removed itself")
}

func TestPTYQuietLaunchScriptIsKeptWhenAskedAndHoldsNoTokenValue(t *testing.T) {
	skipIfWindows(t)
	canFork(t)
	dir, err := os.MkdirTemp("", "q5911")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	t.Setenv("CORAL_KEEP_LAUNCH_SCRIPTS", "1")
	terminal, backend := newTestTerminal(t)
	spawnTestSession(t, backend, "quiet-keep")
	tokenFile := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("s3cr3t-token-value\n"), 0o600))
	line := `export CORAL_SESSION_TOKEN="$(cat '` + tokenFile + `')"; rm -f '` + tokenFile + `'; echo kept-$((6*7))`
	require.NoError(t, terminal.SendQuietLaunchToTarget(context.Background(), terminalinput.Internal, "quiet-keep", line))
	waitForPTYOutput(t, backend, "quiet-keep", "kept-42", 10*time.Second)
	kept, _ := filepath.Glob(filepath.Join(dir, "coral-command-*.sh"))
	require.Len(t, kept, 1, "kept for debugging")
	body, err := os.ReadFile(kept[0])
	require.NoError(t, err)
	assert.Contains(t, string(body), "echo kept-$((6*7))", "the script holds what the line held")
	assert.NotContains(t, string(body), "s3cr3t-token-value", "the token's value is never in the script: it names the file the shell reads")
	assert.Contains(t, string(body), tokenFile)
}

// Only Coral can restart the replay ring: output that prints ESC[3J by itself, from a program in the terminal (an agent included), must not erase what came before it from what a
// person is shown on reconnect (#5911, Security's review). A quiet launch arms the reset once, for a short window.
func TestAClearInOutputRestartsTheReplayOnlyOnceAndOnlyWhenCoralArmedIt(t *testing.T) {
	skipIfWindows(t)
	canFork(t)
	run := func(t *testing.T, name string, arm func(*PTYBackend)) string {
		t.Helper()
		terminal, backend := newTestTerminal(t)
		spawnTestSession(t, backend, name)
		if arm != nil {
			arm(backend)
		}
		require.NoError(t, terminal.SendToTarget(context.Background(), terminalinput.Unclassified, name,
			"echo ran-$((6*7))-evil; printf '\\033[3J'; echo after-$((6*7))-clear"))
		return waitForPTYOutput(t, backend, name, "after-42-clear", 10*time.Second)
	}
	// not armed: an agent's own ESC[3J erases nothing from the replay
	got := run(t, "clear-unarmed", nil)
	assert.Contains(t, got, "ran-42-evil", "what came before an unarmed clear stays in the replay")
	assert.Contains(t, got, "after-42-clear")

	// armed by Coral: the first clear restarts it, and only the first
	got = run(t, "clear-armed", func(b *PTYBackend) { b.ArmReplayClear("clear-armed") })
	assert.NotContains(t, got, "ran-42-evil", "armed: the clear restarts the replay")
	assert.Contains(t, got, "after-42-clear")
	terminal, backend := newTestTerminal(t)
	spawnTestSession(t, backend, "clear-twice")
	backend.ArmReplayClear("clear-twice")
	require.NoError(t, terminal.SendToTarget(context.Background(), terminalinput.Unclassified, "clear-twice", "printf '\\033[3J'; echo first-$((6*7))-kept"))
	waitForPTYOutput(t, backend, "clear-twice", "first-42-kept", 10*time.Second)
	require.NoError(t, terminal.SendToTarget(context.Background(), terminalinput.Unclassified, "clear-twice", "printf '\\033[3J'; echo second-$((6*7))-kept"))
	got = waitForPTYOutput(t, backend, "clear-twice", "second-42-kept", 10*time.Second)
	assert.Contains(t, got, "first-42-kept", "one-shot: the second clear erases nothing")

	// armed, but the window has passed: nothing is reset
	got = run(t, "clear-late", func(b *PTYBackend) { b.clearWindow = -time.Second; b.ArmReplayClear("clear-late") })
	assert.Contains(t, got, "ran-42-evil", "an arming that has expired resets nothing")
}
