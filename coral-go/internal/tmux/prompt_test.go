package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const promptTestSession = "claude-5b8f1c2e-9d3a-4e7b-8c11-0a2b3c4d5e6f"

func TestSanitizePasteKeepsTextButRemovesControlInput(t *testing.T) {
	for in, want := range map[string]string{
		"a\nb\n\tc":                    "a\nb\n\tc",
		"crlf\r\nline\rmac":            "crlf\nline\nmac",
		"esc\x1b[201~inject":           "esc[201~inject",
		"nul\x00bell\acontrol\x7f":     "nulbellcontrol",
		"unicode — ✓ é":                "unicode — ✓ é",
		"c1\u0085\u009bstripped":       "c1stripped",
		"[Coral UI panel request x] y": "[Coral UI panel request x] y",
	} {
		require.Equal(t, want, SanitizePaste(in), "%q", in)
	}
}

// fakeTmux is a tmux stand-in that records every invocation (and load-buffer
// stdin) so the exact protocol can be asserted without a tmux server.
func fakeTmux(t *testing.T) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stdin := filepath.Join(dir, "stdin.txt")
	script := `#!/bin/sh
echo "$*" >> ` + log + `
case "$1" in
  list-panes) echo "` + promptTestSession + `|` + promptTestSession + `:0.0|/tmp/work|agent" ;;
  load-buffer) [ -f ` + dir + `/fail-load ] && exit 1; cat > ` + stdin + ` ;;
  paste-buffer) echo pasted > ` + dir + `/pasted; [ -f ` + dir + `/fail-paste ] && exit 1 ;;
  send-keys) [ -f ` + dir + `/fail-enter ] && exit 1 ;;
esac
exit 0
`
	bin := filepath.Join(dir, "tmux")
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	return &Client{TmuxBin: bin, sessionSockets: map[string]string{}}, dir
}

func readCalls(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func fastSubmit(t *testing.T) {
	t.Helper()
	old := promptSubmitDelay
	promptSubmitDelay = time.Millisecond
	t.Cleanup(func() { promptSubmitDelay = old })
}

func TestSendPromptPastesOnceThenEnterWithoutScriptHandoff(t *testing.T) {
	fastSubmit(t)
	c, dir := fakeTmux(t)
	// Far beyond the 900-byte limit that SendKeys turns into a shell script.
	text := strings.Repeat("line of a long panel request\n", 400) + "end"
	require.Greater(t, len(text), 900)
	require.NoError(t, c.SendPrompt(WithBracketedPasteHint(context.Background(), true), "agent", text, "claude", "5b8f1c2e-9d3a-4e7b-8c11-0a2b3c4d5e6f"))

	calls := readCalls(t, dir)
	var kinds []string
	for _, call := range calls {
		kinds = append(kinds, strings.Fields(call)[0])
	}
	require.Equal(t, []string{"list-panes", "load-buffer", "paste-buffer", "send-keys"}, kinds)
	require.Contains(t, calls[2], "-p", "paste-buffer must use bracketed paste")
	require.Contains(t, calls[2], "-d", "the prompt buffer is deleted after pasting")
	require.Contains(t, calls[2], "-r", "newlines stay LF inside the paste")
	require.True(t, strings.HasSuffix(calls[3], "Enter"), calls[3])
	require.NotContains(t, strings.Join(calls, "\n"), "send-keys -t "+promptTestSession+":0.0 -l", "no literal line-by-line typing")

	got, err := os.ReadFile(filepath.Join(dir, "stdin.txt"))
	require.NoError(t, err)
	require.Equal(t, text, string(got), "the whole prompt is loaded as one buffer, unflattened and not replaced by a script path")
	require.NotContains(t, string(got), "coral-command-")
}

func TestSendPromptRefusesWithoutBracketedPasteAndTypesNothing(t *testing.T) {
	fastSubmit(t)
	c, dir := fakeTmux(t)
	ctx := WithBracketedPasteHint(context.Background(), false)
	err := c.SendPrompt(ctx, "agent", "a\nb", "claude", "5b8f1c2e-9d3a-4e7b-8c11-0a2b3c4d5e6f")
	require.True(t, errors.Is(err, ErrBracketedPasteUnavailable), "got %v", err)
	for _, call := range readCalls(t, dir) {
		k := strings.Fields(call)[0]
		require.Equal(t, "list-panes", k, "only the read-only pane lookup may run: %s", call)
	}
}

func TestSendPromptStripsEscapesBeforeLoadingTheBuffer(t *testing.T) {
	fastSubmit(t)
	c, dir := fakeTmux(t)
	require.NoError(t, c.SendPrompt(WithBracketedPasteHint(context.Background(), true), "agent", "x\x1b[201~y\nz", "claude", "5b8f1c2e-9d3a-4e7b-8c11-0a2b3c4d5e6f"))
	got, err := os.ReadFile(filepath.Join(dir, "stdin.txt"))
	require.NoError(t, err)
	require.Equal(t, "x[201~y\nz", string(got))
}

// Real tmux on a private socket, driving a harmless `cat -v` program that
// enables bracketed paste (like an agent TUI). No agent is involved.
func TestSendPromptWithRealTmuxIsOneBracketedPaste(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	sid := "5b8f1c2e-9d3a-4e7b-8c11-0a2b3c4d5e6f"
	name := "claude-" + sid
	run := func(args ...string) (string, error) {
		out, err := exec.Command(c.TmuxBin, append([]string{"-S", c.SocketPath}, args...)...).CombinedOutput()
		return string(out), err
	}
	_, err := run("new-session", "-d", "-s", name, "-x", "200", "-y", "50", `sh -c 'printf "\033[?2004h"; stty raw -echo; exec cat -v'`)
	require.NoError(t, err)
	time.Sleep(400 * time.Millisecond)

	text := "first line\nsecond \"quoted\" line\n\nlast"
	require.NoError(t, c.SendPrompt(WithBracketedPasteHint(ctx, true), "agent", text, "claude", sid))
	time.Sleep(400 * time.Millisecond)
	screen, err := run("capture-pane", "-p", "-t", name)
	require.NoError(t, err)
	compact := strings.Join(strings.Fields(screen), " ")
	// cat -v renders ESC as ^[ and CR as ^M: one paste, then a single Enter.
	require.Contains(t, compact, "^[[200~first line")
	require.Contains(t, compact, `second "quoted" line`)
	require.Contains(t, compact, "last^[[201~^M")
	require.Equal(t, 1, strings.Count(compact, "^[[200~"))
	require.Equal(t, 1, strings.Count(compact, "^M"), "exactly one Enter: pasted newlines are not CRs")
}

func TestSendPromptWithRealTmuxRefusesWhenPasteModeIsOff(t *testing.T) {
	c := newTestClient(t)
	sid := "6c9f2d3f-ae4b-4f8c-9d22-1b3c4d5e6f70"
	name := "claude-" + sid
	run := func(args ...string) (string, error) {
		out, err := exec.Command(c.TmuxBin, append([]string{"-S", c.SocketPath}, args...)...).CombinedOutput()
		return string(out), err
	}
	_, err := run("new-session", "-d", "-s", name, "-x", "200", "-y", "50", `sh -c 'stty raw -echo; exec cat -v'`)
	require.NoError(t, err)
	time.Sleep(400 * time.Millisecond)

	err = c.SendPrompt(WithBracketedPasteHint(context.Background(), false), "agent", "a\nb", "claude", sid)
	require.True(t, errors.Is(err, ErrBracketedPasteUnavailable), "got %v", err)
	time.Sleep(200 * time.Millisecond)
	screen, _ := run("capture-pane", "-p", "-t", name)
	require.Empty(t, strings.TrimSpace(screen), "nothing may be typed into the pane")
}

func kinds(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, call := range readCalls(t, dir) {
		out = append(out, strings.Fields(call)[0])
	}
	return out
}

const promptSID = "5b8f1c2e-9d3a-4e7b-8c11-0a2b3c4d5e6f"

// Cancelled before the first call: not even the pane lookup runs, and the error
// is plain (retry-safe), never delivery-unknown.
func TestSendPromptCancelledBeforeStartRunsNothing(t *testing.T) {
	fastSubmit(t)
	c, dir := fakeTmux(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.SendPrompt(WithBracketedPasteHint(ctx, true), "agent", "a\nb", "claude", promptSID)
	require.True(t, errors.Is(err, context.Canceled) && !errors.Is(err, ErrDeliveryUnknown), "got %v", err)
	require.Empty(t, readCalls(t, dir), "no tmux command may run for a cancelled prompt")
}

// Cancelled right after the paste ran: Enter still goes out and the call
// succeeds, so the caller records delivery and nothing is replayed.
func TestSendPromptCancelledAfterPasteStillSubmits(t *testing.T) {
	old := promptSubmitDelay
	promptSubmitDelay = 150 * time.Millisecond
	t.Cleanup(func() { promptSubmitDelay = old })
	c, dir := fakeTmux(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; i < 400; i++ {
			if _, err := os.Stat(filepath.Join(dir, "pasted")); err == nil {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	require.NoError(t, c.SendPrompt(WithBracketedPasteHint(ctx, true), "agent", "a\nb", "claude", promptSID))
	require.Error(t, ctx.Err(), "the request context was cancelled mid-delivery")
	require.Equal(t, []string{"list-panes", "load-buffer", "paste-buffer", "send-keys"}, kinds(t, dir))
}

func TestSendPromptFailuresAfterInputStartedAreUnknownAndNotRetryable(t *testing.T) {
	fastSubmit(t)
	for name, marker := range map[string]string{"paste-buffer fails": "fail-paste", "Enter fails": "fail-enter"} {
		c, dir := fakeTmux(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, marker), nil, 0o600))
		err := c.SendPrompt(WithBracketedPasteHint(context.Background(), true), "agent", "a\nb", "claude", promptSID)
		require.True(t, errors.Is(err, ErrDeliveryUnknown), "%s: got %v", name, err)
	}
	// Before any input (buffer load) a failure is a plain, retry-safe error.
	c, dir := fakeTmux(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fail-load"), nil, 0o600))
	err := c.SendPrompt(WithBracketedPasteHint(context.Background(), true), "agent", "a\nb", "claude", promptSID)
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrDeliveryUnknown), "got %v", err)
	require.NotContains(t, kinds(t, dir), "paste-buffer")
	require.NotContains(t, kinds(t, dir), "send-keys")
}

// Two prompts to one pane never interleave: paste, Enter, paste, Enter.
func TestSendPromptSerializesConcurrentPromptsPerPane(t *testing.T) {
	old := promptSubmitDelay
	promptSubmitDelay = 80 * time.Millisecond
	t.Cleanup(func() { promptSubmitDelay = old })
	c, dir := fakeTmux(t)
	done := make(chan error, 2)
	for _, text := range []string{"first\nprompt", "second\nprompt"} {
		go func() {
			done <- c.SendPrompt(WithBracketedPasteHint(context.Background(), true), "agent", text, "claude", promptSID)
		}()
	}
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	var actions []string
	for _, k := range kinds(t, dir) {
		if k == "paste-buffer" || k == "send-keys" {
			actions = append(actions, k)
		}
	}
	require.Equal(t, []string{"paste-buffer", "send-keys", "paste-buffer", "send-keys"}, actions, "prompts interleaved")
}

func TestSendPromptUnknownPasteModeWritesNothing(t *testing.T) {
	c, dir := fakeTmux(t)
	err := c.SendPrompt(context.Background(), "agent", "a\nb", "claude", promptSID)
	require.ErrorIs(t, err, ErrBracketedPasteUnavailable)
	for _, call := range readCalls(t, dir) {
		require.Equal(t, "list-panes", strings.Fields(call)[0])
	}
}
