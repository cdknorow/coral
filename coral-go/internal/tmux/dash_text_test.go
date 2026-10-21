package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// #3690 (#3689 finding 1): text starting with '-' is typed as text, not read as tmux options
// (which dropped "- fix the tests" and then sent Enter on its own). The pane runs cat, so each
// line typed comes back.
func TestDashLeadingTextIsTyped(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	createTestSession(t, c, "dash-text", t.TempDir())
	target := "dash-text:0.0"
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, target, "exec cat"))

	lines := []string{"- fix the tests", "--help", "-N 3 abc", "-X cancel", "-t elsewhere", "-1"}
	for _, l := range lines {
		require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, target, l), l)
	}
	require.NoError(t, c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, target, "-l raw"))
	require.NoError(t, c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, target, "\r"))

	want := append(lines, "-l raw")
	var out string
	assert.Eventually(t, func() bool {
		out, _ = c.CapturePaneTarget(ctx, target, 50)
		for _, l := range want {
			if strings.Count(out, l) < 2 { // typed, and cat's echo of it
				return false
			}
		}
		return true
	}, 5*time.Second, 50*time.Millisecond)
	for _, l := range want {
		assert.GreaterOrEqual(t, strings.Count(out, l), 2, "%q typed and echoed:\n%s", l, out)
	}
}

// A session variable whose value starts with '-' is set as given (tmux stops reading options at
// the name, so this held before the "--" too; the test keeps it so).
func TestSetEnvironmentTakesADashLeadingValue(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	createTestSession(t, c, "dash-env", t.TempDir())
	require.NoError(t, c.SetEnvironment(ctx, "dash-env", "CORAL_TEST_DASH", "-u nobody"))
	out, err := exec.Command(c.TmuxBin, "-S", c.SocketPath, "show-environment", "-t", "dash-env", "CORAL_TEST_DASH").CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, "CORAL_TEST_DASH=-u nobody", strings.TrimSpace(string(out)))
}

// When typing the text fails, no Enter follows: it would submit whatever was already in the
// agent's input instead. A stand-in tmux fails the literal send and records every call.
func TestNoEnterAfterAFailedSend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell-script tmux")
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "tmux")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\necho \"$*\" >> '"+calls+"'\nfor a in \"$@\"; do [ \"$a\" = -l ] && exit 1; done\nexit 0\n"), 0o700))
	c := &Client{TmuxBin: fake, SocketPath: filepath.Join(dir, "s"), sessionSockets: map[string]string{}}
	restore := SetSendKeysSettleForTest(time.Millisecond)
	defer restore()

	err := c.SendKeysToTarget(context.Background(), terminalinput.Unclassified, "agent:0.0", "- fix the tests")
	require.Error(t, err)
	got, rerr := os.ReadFile(calls)
	require.NoError(t, rerr)
	assert.Contains(t, string(got), "send-keys -t agent:0.0 -l -- - fix the tests")
	for _, call := range strings.Split(strings.TrimSpace(string(got)), "\n") {
		assert.False(t, strings.HasSuffix(call, " Enter"), "no Enter after the failed send: %s", call)
	}
	assert.Equal(t, 1, strings.Count(string(got), "send-keys"), "only the failed send")
}
