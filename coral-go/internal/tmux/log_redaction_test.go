//go:build !windows

package tmux

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// failingTmux is a tmux that fails every command, the way tmux does when its server or pane is
// gone, and repeats a key it doesn't know (as real tmux does).
func failingTmux(t *testing.T) *Client {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tmux")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\ncat >/dev/null 2>&1 &\necho 'unknown key: HUNTER2-KEY' >&2\nexit 1\n"), 0o700))
	return &Client{TmuxBin: bin, SocketPath: filepath.Join(t.TempDir(), "s")}
}

// #4112 (Security's #4107 audit, finding 1): a failed tmux command logs its subcommand, target,
// error and stderr, never what was typed, pasted or launched. Before, every failed send logged
// its whole argument list, so terminal keystrokes, prompts and board notices reached coral.log.
func TestFailedTmuxCommandsNeverLogWhatWasTyped(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	c := failingTmux(t)
	ctx := context.Background()
	const secret = "typed-secret-4112"

	_ = c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, "claude-x:0.0", "echo "+secret)       // a person's keystrokes
	_ = c.SendKeysToTarget(ctx, terminalinput.Unclassified, "claude-x:0.0", "please run "+secret)          // a typed command
	_ = c.SendRawKeys(ctx, terminalinput.Unclassified, "claude-x", []string{"HUNTER2-KEY"}, "claude", "x") // a raw key the request named
	_, _ = c.run(ctx, "new-session", "-d", "-s", "claude-x", "claude --prompt '"+secret+"'")               // a launch line
	_, _ = c.run(ctx, "set-buffer", "-b", "coral-x", secret)

	out := buf.String()
	require.Contains(t, out, "[tmux] exec failed", "the failures are still logged")
	assert.Contains(t, out, "cmd=send-keys")
	assert.Contains(t, out, `target="claude-x:0.0"`)
	assert.Contains(t, out, "cmd=new-session")
	assert.NotContains(t, out, secret, "no typed, pasted or launched text")
	assert.NotContains(t, out, "HUNTER2-KEY", "no key name, not even tmux's own repeat of it")
	assert.NotContains(t, out, "please run")

	err := c.SendRawKeys(ctx, terminalinput.Unclassified, "claude-x", []string{"HUNTER2-KEY"}, "claude", "x")
	if err != nil {
		assert.NotContains(t, err.Error(), "HUNTER2-KEY", "the error (which callers log) doesn't carry the key either")
	}
}

func TestDescribeTmuxCommand(t *testing.T) {
	got := describeTmuxCommand([]string{"-S", "/s", "-f", "/dev/null", "send-keys", "-t", "a:0.0", "-l", "--", "hello"})
	assert.Equal(t, `cmd=send-keys target="a:0.0" other_args=3 bytes=9`, got)
	assert.Equal(t, `cmd=list-panes target="" other_args=0 bytes=0`, describeTmuxCommand([]string{"list-panes"}))
	assert.Equal(t, "cmd=none", describeTmuxCommand(nil))
	assert.False(t, strings.Contains(describeTmuxCommand([]string{"set-buffer", "-b", "b", "SECRET"}), "SECRET"))
}

// #4112 fix 2: the same failure again and again (a send to a pane that is gone) logs one line,
// then one with the count after the window, instead of a line per send (100 sends gave 200 lines).
func TestRepeatedTmuxFailuresAreCoalesced(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	at := time.Now()
	var mu sync.Mutex
	execFailures.SetClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return at })
	t.Cleanup(func() { execFailures.SetClock(time.Now) })
	c := failingTmux(t)
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		_ = c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, "claude-gone:0.0", "x")
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "[tmux] exec failed"), "one line for 100 identical failures")
	before := strings.Count(buf.String(), "\n")
	for i := 0; i < 100; i++ {
		_ = c.SendKeysToTarget(ctx, terminalinput.Unclassified, "claude-gone2:0.0", "echo hi")
	}
	assert.Equal(t, before+2, strings.Count(buf.String(), "\n"), "a failing send-keys: its exec line and its send-keys line once, not 200 lines")
	_ = c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, "claude-other:0.0", "x")
	assert.Equal(t, 3, strings.Count(buf.String(), "[tmux] exec failed"), "another pane's failure is its own line (after claude-gone and claude-gone2)")

	mu.Lock()
	at = at.Add(execFailureEvery)
	mu.Unlock()
	_ = c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, "claude-gone:0.0", "x")
	assert.Contains(t, buf.String(), "(and 99 more like it in the last 5m0s)")
}
