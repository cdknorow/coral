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
		got := string(restartRingAtClear(append([]byte(nil), tc.ring...), tc.n))
		assert.Equal(t, tc.want, got, tc.name)
	}
	// a read that completes a sequence begun in the one before
	ring := []byte("before\x1b[3")
	ring = append(ring, 'J', 'x')
	assert.Equal(t, seq+"x", string(restartRingAtClear(ring, 2)), "straddling")
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
