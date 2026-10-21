package tmux

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

// #5171: a restarted agent was left at a bare shell with ". '/tmp/coral-command-N.sh'" typed and unsubmitted, and the next text typed into the
// pane (a board nudge) was appended to it: ".: no such file or directory: ...shYou have 1 unread message". The launch line was typed, its Enter
// could not be sent (the request's context ended, a prompt held it), and the failure removed the script but left the typed line in the shell.

// longLaunch is a command over the inline limit that writes marker when it runs.
func longLaunch(marker string) string {
	return "echo LAUNCHED > " + shellQuote(marker) + " # " + strings.Repeat("x", inlineCommandLimit+100)
}

func paneText(t *testing.T, c *Client, target string) string {
	t.Helper()
	s, err := c.CapturePaneTarget(context.Background(), target, 200)
	require.NoError(t, err)
	return s
}

// The request's context ends between the typing and the Enter: the line is already in the shell, so the Enter is still sent and the launch runs.
func TestLaunchLineIsSubmittedWhenTheContextEndsAfterTheTyping(t *testing.T) {
	c := newTestClient(t)
	t.Setenv("TMPDIR", t.TempDir())
	createTestSession(t, c, "launch-cancel", t.TempDir())
	marker := filepath.Join(t.TempDir(), "marker")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithBeforeEnter(ctx, func(context.Context) bool { cancel(); return true }) // the request ends just before the Enter
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, "launch-cancel:0.0", longLaunch(marker)), "the typed line is submitted whatever happened to the request")

	require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 5*time.Second, 50*time.Millisecond, "the launch command ran")
	assert.Empty(t, tempHandoffs(t), "the script removed itself")
	// Text typed afterwards is its own line.
	require.NoError(t, c.SendKeysToTarget(context.Background(), terminalinput.Unclassified, "launch-cancel:0.0", "echo AFTER_$((20+22))"))
	require.Eventually(t, func() bool { return strings.Contains(paneText(t, c, "launch-cancel:0.0"), "AFTER_42") }, 5*time.Second, 50*time.Millisecond)
	assert.NotContains(t, paneText(t, c, "launch-cancel:0.0"), "no such file")
}

// An Enter that is withheld (a prompt is open) or cannot be sent leaves no half-typed launch line behind: the script is gone, so the line
// would fail, and the next text typed would run on after it.
func TestLaunchLineIsClearedWhenItsEnterIsWithheld(t *testing.T) {
	c := newTestClient(t)
	t.Setenv("TMPDIR", t.TempDir())
	createTestSession(t, c, "launch-held", t.TempDir())
	marker := filepath.Join(t.TempDir(), "marker")

	ctx := WithBeforeEnter(context.Background(), func(context.Context) bool { return false })
	require.ErrorIs(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, "launch-held:0.0", longLaunch(marker)), ErrEnterWithheld)
	assert.Empty(t, tempHandoffs(t), "the script is removed")

	// The next text typed into the pane (a nudge) must not be appended to the abandoned line.
	require.NoError(t, c.SendKeysToTarget(context.Background(), terminalinput.Unclassified, "launch-held:0.0", "echo NUDGE_$((20+22))"))
	require.Eventually(t, func() bool { return strings.Contains(paneText(t, c, "launch-held:0.0"), "NUDGE_42") }, 5*time.Second, 50*time.Millisecond)
	text := paneText(t, c, "launch-held:0.0")
	assert.NotContains(t, text, "no such file", "the nudge ran as its own line")
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "the withheld launch did not run")
}
