package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// newTestClient creates a tmux Client with an isolated socket for testing.
// Skips if tmux is not available. Cleans up on test completion.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}
	// Use a short socket path under os.TempDir — t.TempDir() paths include
	// the test name and can exceed the Unix socket 104-char limit.
	dir, err := os.MkdirTemp("", "coral-tmux-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "t.sock")

	// Smoke-test that tmux can actually create a detached session on this
	// socket. Some CI sandboxes reject new-session with exit 1 — skip rather
	// than fail on an environmental issue.
	// On its OWN socket: kill-server returns before that server has exited, and a test's first
	// new-session on the same socket could reach the dying server ("server exited unexpectedly").
	smoke := filepath.Join(dir, "smoke.sock")
	if out, err := exec.Command(bin, "-S", smoke, "new-session", "-d", "-s", "coral-smoke", "sleep 1").CombinedOutput(); err != nil {
		exec.Command(bin, "-S", smoke, "kill-server").Run()
		t.Skipf("tmux cannot create sessions in this environment: %v (output: %s)", err, strings.TrimSpace(string(out)))
	}
	exec.Command(bin, "-S", smoke, "kill-server").Run()

	c := &Client{
		TmuxBin:           bin,
		SocketPath:        sock,
		FallbackToDefault: false,
		sessionSockets:    make(map[string]string),
	}
	t.Cleanup(func() {
		exec.Command(bin, "-S", sock, "kill-server").Run()
	})
	return c
}

// createTestSession creates a tmux session and waits for it to be ready.
func createTestSession(t *testing.T, c *Client, name, workDir string) {
	t.Helper()
	ctx := context.Background()
	err := c.NewSession(ctx, name, workDir)
	require.NoError(t, err)
	// tmux reads pane_current_path from the pane's foreground process, which can be
	// briefly unreadable while the shell starts on a loaded machine. Wait for the pane
	// to report it rather than a fixed delay.
	require.Eventually(t, func() bool {
		panes, _ := c.ListPanes(ctx)
		for _, p := range panes {
			if p.SessionName == name && p.CurrentPath != "" {
				return true
			}
		}
		return false
	}, 5*time.Second, 25*time.Millisecond, "pane %q never reported its path", name)
}

// ── Unit Tests ─────────────────────────────────────────────────────────

func TestSessionFromTarget(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-abc:0.0", "claude-abc"},
		{"name.0", "name"},
		{"bare-name", "bare-name"},
		{"session:1.2", "session"},
		{"a.b", "a"},
		{"=claude-abc", "claude-abc"},
		{"=claude-abc:0.0", "claude-abc"},
	}
	for _, tt := range tests {
		got := sessionFromTarget(tt.input)
		assert.Equal(t, tt.want, got, "sessionFromTarget(%q)", tt.input)
	}
}

func TestAttachCommand_WithSocket(t *testing.T) {
	c := &Client{TmuxBin: "tmux", SocketPath: "/tmp/test.sock"}
	cmd := c.AttachCommand("my-session")
	assert.Equal(t, "tmux -S /tmp/test.sock attach -t my-session", cmd)
}

func TestAttachCommand_NoSocket(t *testing.T) {
	c := &Client{TmuxBin: "tmux", SocketPath: ""}
	cmd := c.AttachCommand("my-session")
	assert.Equal(t, "tmux attach -t my-session", cmd)
}

// ── Integration Tests: Session Lifecycle ────────────────────────────────

func TestClient_NewSession_HasSession_KillSession(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	dir := t.TempDir()

	// Create session
	err := c.NewSession(ctx, "test-lifecycle", dir)
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	// HasSession should return true
	assert.True(t, c.HasSession(ctx, "test-lifecycle"))

	// Kill via kill-session directly
	_, err = c.run(ctx, "kill-session", "-t", "test-lifecycle")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	// HasSession should return false
	assert.False(t, c.HasSession(ctx, "test-lifecycle"))
}

func TestClient_KillSession_NotFound(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// KillSession on nonexistent should error
	err := c.KillSession(ctx, "nonexistent", "", "")
	assert.Error(t, err)
}

// ── Integration Tests: ListPanes ────────────────────────────────────────

func TestClient_ListPanes_Empty(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// No sessions created yet — should return empty or nil
	panes, err := c.ListPanes(ctx)
	require.NoError(t, err)
	assert.Empty(t, panes)
}

func TestClient_ListPanes_WithSessions(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "pane-list-a", t.TempDir())
	createTestSession(t, c, "pane-list-b", t.TempDir())

	panes, err := c.ListPanes(ctx)
	require.NoError(t, err)
	assert.Len(t, panes, 2)

	names := make(map[string]bool)
	for _, p := range panes {
		names[p.SessionName] = true
		assert.NotEmpty(t, p.Target)
		assert.NotEmpty(t, p.CurrentPath)
	}
	assert.True(t, names["pane-list-a"])
	assert.True(t, names["pane-list-b"])
}

// ── Integration Tests: FindPane ─────────────────────────────────────────

func TestClient_FindPane_BySessionID(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "claude-abc123def456", t.TempDir())

	pane, err := c.FindPane(ctx, "anything", "claude", "abc123def456")
	require.NoError(t, err)
	require.NotNil(t, pane)
	assert.Equal(t, "claude-abc123def456", pane.SessionName)
}

// A bare name finds nothing, even one in a pane's title (#3687: an agent sets its own title).
func TestClient_FindPane_ByNameFindsNothing(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "my-agent-session", t.TempDir())
	c.SetPaneTitle(ctx, "my-agent-session.0", "my-repo \u2014 claude")
	time.Sleep(100 * time.Millisecond)

	pane, err := c.FindPane(ctx, "my-repo", "", "")
	require.NoError(t, err)
	assert.Nil(t, pane)
}

func TestClient_FindPane_NotFound(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	pane, err := c.FindPane(ctx, "ghost", "", "")
	require.NoError(t, err)
	assert.Nil(t, pane)
}

// ── Integration Tests: SendKeys + CapturePane ──────────────────────────

func TestClient_SendKeys_CapturePane(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "send-capture", t.TempDir())

	// Send a command
	err := c.SendKeysToTarget(ctx, terminalinput.Unclassified, "send-capture:0.0", "echo TMUX_MARKER_42")
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	// Capture pane content
	content, err := c.CapturePaneTarget(ctx, "send-capture:0.0", 200)
	require.NoError(t, err)
	assert.Contains(t, content, "TMUX_MARKER_42")
}

func TestClient_SendKeys_MultiLine(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "multiline-test", t.TempDir())

	// Send multi-line via paste-buffer path
	err := c.SendKeysToTarget(ctx, terminalinput.Unclassified, "multiline-test:0.0", "echo LINE_A\necho LINE_B")
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	content, err := c.CapturePaneTarget(ctx, "multiline-test:0.0", 200)
	require.NoError(t, err)
	assert.Contains(t, content, "LINE_A")
	assert.Contains(t, content, "LINE_B")
}

// ── Integration Tests: RespawnPane ──────────────────────────────────────

func TestClient_RespawnPane(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	dir := t.TempDir()

	createTestSession(t, c, "respawn-test", dir)

	// Send initial command
	c.SendKeysToTarget(ctx, terminalinput.Unclassified, "respawn-test:0.0", "echo BEFORE_RESPAWN")
	time.Sleep(500 * time.Millisecond)

	// Respawn the pane
	err := c.RespawnPane(ctx, "respawn-test:0.0", dir)
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	// Send new command
	c.SendKeysToTarget(ctx, terminalinput.Unclassified, "respawn-test:0.0", "echo AFTER_RESPAWN")
	time.Sleep(500 * time.Millisecond)

	content, err := c.CapturePaneTarget(ctx, "respawn-test:0.0", 200)
	require.NoError(t, err)
	assert.Contains(t, content, "AFTER_RESPAWN")
}

// ── Integration Tests: RenameSession ────────────────────────────────────

func TestClient_RenameSession(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "old-name", t.TempDir())

	err := c.RenameSession(ctx, "old-name", "new-name")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	assert.True(t, c.HasSession(ctx, "new-name"))
	assert.False(t, c.HasSession(ctx, "old-name"))
}

// ── Integration Tests: Resize ───────────────────────────────────────────

func TestClient_ResizePane(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "claude-00003687-0040-4000-8000-000000000000", t.TempDir())

	// Resize should not error
	target, err := c.FindPaneTarget(ctx, "claude-00003687-0040-4000-8000-000000000000", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, target)

	err = c.ResizePaneTarget(ctx, target, 120, 40)
	require.NoError(t, err)
}

func TestClient_ResizePaneTarget_RowsAndCols(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "claude-00003687-0041-4000-8000-000000000000", t.TempDir())

	target, err := c.FindPaneTarget(ctx, "claude-00003687-0041-4000-8000-000000000000", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, target)

	// Resize to specific dimensions
	err = c.ResizePaneTarget(ctx, target, 100, 30)
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	// Verify tmux reports the correct dimensions
	out, err := c.DisplayMessage(ctx, target, "#{window_width},#{window_height}")
	require.NoError(t, err)
	parts := strings.SplitN(strings.TrimSpace(out), ",", 2)
	require.Len(t, parts, 2, "expected width,height from display-message")
	assert.Equal(t, "100", parts[0], "columns should be 100")
	assert.Equal(t, "30", parts[1], "rows should be 30")

	// Resize again with different rows to verify rows actually change
	err = c.ResizePaneTarget(ctx, target, 100, 50)
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	out, err = c.DisplayMessage(ctx, target, "#{window_width},#{window_height}")
	require.NoError(t, err)
	parts = strings.SplitN(strings.TrimSpace(out), ",", 2)
	require.Len(t, parts, 2)
	assert.Equal(t, "100", parts[0], "columns should still be 100")
	assert.Equal(t, "50", parts[1], "rows should be updated to 50")
}

func TestClient_ResizePaneTarget_RowsZeroSkipped(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "claude-00003687-0042-4000-8000-000000000000", t.TempDir())

	target, err := c.FindPaneTarget(ctx, "claude-00003687-0042-4000-8000-000000000000", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, target)

	// When rows=0, only columns should be resized (backward compat)
	err = c.ResizePaneTarget(ctx, target, 80, 0)
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	out, err := c.DisplayMessage(ctx, target, "#{window_width}")
	require.NoError(t, err)
	assert.Equal(t, "80", strings.TrimSpace(out), "columns should be 80")
}

// ── Integration Tests: ClearHistory ─────────────────────────────────────

func TestClient_ClearHistory(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "clear-hist", t.TempDir())

	// Generate scrollback: 50 lines is more than the pane shows.
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, "clear-hist:0.0", "for i in $(seq 1 50); do echo SCROLLBACK_LINE; done"))

	// Capture with large scrollback should have all of it
	// Polled here rather than with assert.Eventually, whose condition runs in its own goroutine and
	// would race this test reading before/captureErr for the failure message.
	var before string
	var captureErr error
	ok := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		before, captureErr = c.CapturePaneTarget(ctx, "clear-hist:0.0", 500)
		if captureErr == nil && strings.Count(before, "SCROLLBACK_LINE\n") >= 50 {
			ok = true
			break
		}
	}
	if !ok {
		// Seen once under heavy load and not reproduced since: say what the pane held, so the next one explains itself.
		tail := before
		if len(tail) > 600 {
			tail = tail[len(tail)-600:]
		}
		t.Fatalf("%d of 50 lines; capture error %v; pane tail:\n%s", strings.Count(before, "SCROLLBACK_LINE\n"), captureErr, tail)
	}

	// Clear history (removes scrollback buffer, not visible content)
	err := c.ClearHistory(ctx, "clear-hist:0.0")
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	// After clearing, capture with scrollback should be shorter
	after, err := c.CapturePaneTarget(ctx, "clear-hist:0.0", 500)
	require.NoError(t, err)
	assert.Less(t, len(strings.TrimSpace(after)), len(strings.TrimSpace(before)),
		"scrollback should be shorter after clear-history")
}

// ── Integration Tests: SetEnvironment ───────────────────────────────────

func TestClient_SetEnvironment(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "env-test", t.TempDir())

	err := c.SetEnvironment(ctx, "env-test", "CORAL_TEST_VAR", "hello123")
	require.NoError(t, err)
}

// ── Integration Tests: PipePane + ClosePipePane ─────────────────────────

func TestClient_PipePane(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "pipe-test", t.TempDir())

	logFile := filepath.Join(t.TempDir(), "pipe-log.txt")
	err := c.PipePane(ctx, "pipe-test:0.0", logFile)
	require.NoError(t, err)

	// Send output that should be logged
	c.SendKeysToTarget(ctx, terminalinput.Unclassified, "pipe-test:0.0", "echo PIPE_MARKER")
	time.Sleep(500 * time.Millisecond)

	// Close pipe
	err = c.ClosePipePane(ctx, "pipe-test:0.0")
	require.NoError(t, err)
}

// ── Integration Tests: SendTerminalInputToTarget ────────────────────────

func TestClient_SendTerminalInputToTarget_Enter(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "terminal-input", t.TempDir())

	// Send Enter key
	err := c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, "terminal-input:0.0", "\r")
	require.NoError(t, err)
}

func TestClient_SendTerminalInputToTarget_CtrlC(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "ctrl-test", t.TempDir())

	// Send Ctrl+C (0x03)
	err := c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, "ctrl-test:0.0", "\x03")
	require.NoError(t, err)
}

func TestClient_SendTerminalInputToTarget_LiteralText(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "literal-test", t.TempDir())

	err := c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, "literal-test:0.0", "hello world")
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)

	content, err := c.CapturePaneTarget(ctx, "literal-test:0.0", 200)
	require.NoError(t, err)
	assert.Contains(t, content, "hello world")
}

// ── Integration Tests: SendRawKeys ──────────────────────────────────────

func TestClient_SendRawKeys(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "claude-00003687-0043-4000-8000-000000000000", t.TempDir())

	// Send raw keys (Enter)
	err := c.SendRawKeys(ctx, terminalinput.Unclassified, "claude-00003687-0043-4000-8000-000000000000", []string{"Enter"}, "", "")
	require.NoError(t, err)
}

func TestClient_SendRawKeys_NotFound(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	err := c.SendRawKeys(ctx, terminalinput.Unclassified, "nonexistent", []string{"Enter"}, "", "")
	assert.Error(t, err)
}

// ── Integration Tests: DisplayMessage ───────────────────────────────────

func TestClient_DisplayMessage(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "display-msg", t.TempDir())

	output, err := c.DisplayMessage(ctx, "display-msg:0.0", "#{pane_width}")
	require.NoError(t, err)
	assert.NotEmpty(t, output)
	// pane_width should be a number
	assert.True(t, len(strings.TrimSpace(output)) > 0)
}

// ── Integration Tests: KillSessionOnly ──────────────────────────────────

func TestClient_KillSessionOnly(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	createTestSession(t, c, "claude-kill-only-test", t.TempDir())

	err := c.KillSessionOnly(ctx, "claude-kill-only-test", "claude", "kill-only-test")
	require.NoError(t, err)

	assert.False(t, c.HasSession(ctx, "claude-kill-only-test"))
}

// A line typed in the dashboard terminal arrives as text ending in \r. A zsh pane
// takes a bracketed paste's \r as pasted text, so the command used to sit on the line
// unsubmitted; /bin/sh submitted it (#3259).
func TestClient_SendTerminalInputToTarget_SubmitsTrailingCR(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not available")
	}
	c := newTestClient(t)
	ctx := context.Background()
	createTestSession(t, c, "zsh-cr", t.TempDir())
	target := "zsh-cr:0.0"

	require.NoError(t, c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, target, "exec env PROMPT='ZSH> ' "+zsh+" -f -i"))
	require.NoError(t, c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, target, "\r"))
	// A line that starts with the prompt: the typed command's echo holds "ZSH> " too, behind the
	// old shell's prompt, before zsh is running (#3788).
	require.Eventually(t, func() bool {
		out, _ := c.CapturePaneTarget(ctx, target, 50)
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "ZSH>") { // the capture drops the trailing space
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "zsh never showed its prompt")

	for _, tc := range []struct{ name, data, want string }{
		{"text and CR", "echo ONE_$((1+1))\r", "ONE_2"},
		{"text and CRLF", "echo TWO_$((1+2))\r\n", "TWO_3"},
		{"two lines then CR", "echo A_$((2+2))\necho B_$((2+3))\r", "B_5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, c.SendTerminalInputToTarget(ctx, terminalinput.Unclassified, target, tc.data))
			require.Eventually(t, func() bool {
				out, _ := c.CapturePaneTarget(ctx, target, 100)
				for _, line := range strings.Split(out, "\n") {
					if strings.TrimSpace(line) == tc.want {
						return true
					}
				}
				return false
			}, 5*time.Second, 50*time.Millisecond, "the command was not submitted to zsh")
		})
	}
}
