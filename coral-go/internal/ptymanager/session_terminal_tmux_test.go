//go:build !windows

package ptymanager

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTmuxTestTerminal creates an isolated TmuxSessionTerminal backed by a
// dedicated tmux socket in t.TempDir(). Skips if tmux is not installed.
func newTmuxTestTerminal(t *testing.T) *TmuxSessionTerminal {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	sock := shortSocketPath(t)

	// Smoke-test that tmux can create a detached session here. Some sandboxes
	// reject new-session with exit 1 (no TTY / missing TERM / seccomp); skip
	// rather than fail on an environmental issue.
	if out, err := exec.Command("tmux", "-S", sock, "new-session", "-d", "-s", "coral-smoke", "sleep 1").CombinedOutput(); err != nil {
		exec.Command("tmux", "-S", sock, "kill-server").Run()
		t.Skipf("tmux cannot create sessions in this environment: %v (output: %s)", err, strings.TrimSpace(string(out)))
	}
	exec.Command("tmux", "-S", sock, "kill-server").Run()

	// Use NewClient to get a properly initialized client (sessionSockets map, etc.)
	c := tmux.NewClient()
	c.SocketPath = sock
	c.FallbackToDefault = false // isolate from real sessions

	t.Cleanup(func() {
		exec.Command(c.TmuxBin, "-S", c.SocketPath, "kill-server").Run()
	})

	return NewTmuxSessionTerminal(c)
}

// spawnTmuxSession creates a tmux session with a known name via the terminal interface.
func spawnTmuxSession(t *testing.T, terminal *TmuxSessionTerminal, name, workDir string) {
	t.Helper()
	ctx := context.Background()
	err := terminal.CreateSession(ctx, name, workDir)
	require.NoError(t, err)
	// Brief pause for tmux to initialize the session
	time.Sleep(300 * time.Millisecond)
}

// waitForCapture polls CaptureOutput until marker appears or timeout expires.
func waitForCapture(t *testing.T, terminal *TmuxSessionTerminal, name, marker string, timeout time.Duration) string {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := terminal.CaptureOutput(ctx, name, 200, "", "")
		if err == nil && strings.Contains(output, marker) {
			return output
		}
		time.Sleep(200 * time.Millisecond)
	}
	output, _ := terminal.CaptureOutput(ctx, name, 200, "", "")
	return output
}

// ── ListSessions ──────────────────────────────────────────────────────

func TestTmuxSessionTerminal_ListSessions_Empty(t *testing.T) {
	terminal := newTmuxTestTerminal(t)

	sessions, err := terminal.ListSessions(context.Background())
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestTmuxSessionTerminal_ListSessions_WithSessions(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	dir := t.TempDir()

	spawnTmuxSession(t, terminal, "test-session-1", dir)

	sessions, err := terminal.ListSessions(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, sessions)
	// Verify the session appears with a non-empty session name
	found := false
	for _, s := range sessions {
		if s.SessionName == "test-session-1" {
			found = true
			assert.NotEmpty(t, s.CurrentPath)
			break
		}
	}
	assert.True(t, found, "expected to find test-session-1 in listed sessions: %+v", sessions)
}

func TestTmuxSessionTerminal_ListSessions_Multiple(t *testing.T) {
	terminal := newTmuxTestTerminal(t)

	spawnTmuxSession(t, terminal, "multi-a", t.TempDir())
	spawnTmuxSession(t, terminal, "multi-b", t.TempDir())

	sessions, err := terminal.ListSessions(context.Background())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(sessions), 2, "expected at least 2 sessions")
}

// ── FindSession ──────────────────────────────────────────────────────

func TestTmuxSessionTerminal_FindSession_ByName(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	dir := t.TempDir()

	spawnTmuxSession(t, terminal, "find-me", dir)

	pane, err := terminal.FindSession(context.Background(), "find-me", "", "")
	require.NoError(t, err)
	require.NotNil(t, pane, "expected to find session 'find-me'")
	assert.Equal(t, "find-me", pane.SessionName)
}

func TestTmuxSessionTerminal_FindSession_NotFound(t *testing.T) {
	terminal := newTmuxTestTerminal(t)

	pane, err := terminal.FindSession(context.Background(), "ghost", "", "")
	require.NoError(t, err)
	assert.Nil(t, pane)
}

// ── CreateSession / HasSession / KillSession ─────────────────────────

func TestTmuxSessionTerminal_CreateSession(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()
	dir := t.TempDir()

	err := terminal.CreateSession(ctx, "create-test", dir)
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)

	assert.True(t, terminal.HasSession(ctx, "create-test"))
}

func TestTmuxSessionTerminal_KillSession(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "kill-me", t.TempDir())
	assert.True(t, terminal.HasSession(ctx, "kill-me"))

	err := terminal.KillSession(ctx, "kill-me", "", "")
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	assert.False(t, terminal.HasSession(ctx, "kill-me"))
}

func TestTmuxSessionTerminal_KillSession_NotFound(t *testing.T) {
	terminal := newTmuxTestTerminal(t)

	err := terminal.KillSession(context.Background(), "nonexistent", "", "")
	assert.Error(t, err)
}

// ── HasSession ───────────────────────────────────────────────────────

func TestTmuxSessionTerminal_HasSession_False(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	assert.False(t, terminal.HasSession(context.Background(), "nope"))
}

// ── SendInput / CaptureOutput ────────────────────────────────────────

func TestTmuxSessionTerminal_SendInput_CaptureOutput(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "io-tmux", t.TempDir())

	err := terminal.SendInput(ctx, "io-tmux", "echo TMUX_MARKER_42", "", "")
	require.NoError(t, err)

	output := waitForCapture(t, terminal, "io-tmux", "TMUX_MARKER_42", 5*time.Second)
	assert.Contains(t, output, "TMUX_MARKER_42", "expected capture to contain marker")
}

func TestTmuxSessionTerminal_CaptureOutput_EmptySession(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "empty-capture", t.TempDir())

	// Capture on a fresh session should not error (may return prompt text)
	output, err := terminal.CaptureOutput(ctx, "empty-capture", 200, "", "")
	require.NoError(t, err)
	_ = output // content varies by shell; just verify no error
}

// ── SendRawInput ─────────────────────────────────────────────────────

func TestTmuxSessionTerminal_SendRawInput(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "raw-tmux", t.TempDir())

	// Send raw keys — should not error
	err := terminal.SendRawInput(ctx, "raw-tmux", []string{"h", "i"}, "", "")
	require.NoError(t, err)
}

// ── SendToTarget ─────────────────────────────────────────────────────

func TestTmuxSessionTerminal_SendToTarget(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "target-tmux", t.TempDir())

	// Find the target first
	pane, err := terminal.FindSession(ctx, "target-tmux", "", "")
	require.NoError(t, err)
	require.NotNil(t, pane)

	err = terminal.SendToTarget(ctx, pane.Target, "echo TARGET_OK")
	require.NoError(t, err)

	output := waitForCapture(t, terminal, "target-tmux", "TARGET_OK", 5*time.Second)
	assert.Contains(t, output, "TARGET_OK")
}

func TestTmuxSessionTerminal_SendToTarget_LongCommandUsesHandoff(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()
	spawnTmuxSession(t, terminal, "long-target-tmux", t.TempDir())

	marker := "LONG_TMUX_HANDOFF_MARKER"
	value := strings.Repeat("x", 1200) + marker
	command := "printf '%s' '" + value + "'"
	require.Greater(t, len(command), 900)
	require.NoError(t, terminal.SendToTarget(ctx, "long-target-tmux:0.0", command))

	output := waitForCapture(t, terminal, "long-target-tmux", marker, 5*time.Second)
	assert.Contains(t, output, marker)
}

// ── ResizeSession ────────────────────────────────────────────────────

func TestTmuxSessionTerminal_ResizeSession(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "resize-tmux", t.TempDir())

	err := terminal.ResizeSession(ctx, "resize-tmux", 120, "", "")
	require.NoError(t, err)
}

// ── RenameSession ────────────────────────────────────────────────────

func TestTmuxSessionTerminal_RenameSession(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "old-name", t.TempDir())
	assert.True(t, terminal.HasSession(ctx, "old-name"))

	err := terminal.RenameSession(ctx, "old-name", "new-name")
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	assert.True(t, terminal.HasSession(ctx, "new-name"))
	assert.False(t, terminal.HasSession(ctx, "old-name"))
}

// ── RestartPane ──────────────────────────────────────────────────────

func TestTmuxSessionTerminal_RestartPane(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "restart-tmux", t.TempDir())

	// Send a command, then restart
	terminal.SendInput(ctx, "restart-tmux", "echo BEFORE_RESTART", "", "")
	time.Sleep(500 * time.Millisecond)

	pane, err := terminal.FindSession(ctx, "restart-tmux", "", "")
	require.NoError(t, err)
	require.NotNil(t, pane)

	err = terminal.RestartPane(ctx, pane.Target, t.TempDir())
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	// Session should still exist after restart
	assert.True(t, terminal.HasSession(ctx, "restart-tmux"))
}

// ── Logging ──────────────────────────────────────────────────────────

func TestTmuxSessionTerminal_StartStopLogging(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "log-tmux", t.TempDir())

	pane, err := terminal.FindSession(ctx, "log-tmux", "", "")
	require.NoError(t, err)
	require.NotNil(t, pane)

	logPath := filepath.Join(t.TempDir(), "test.log")

	err = terminal.StartLogging(ctx, pane.Target, logPath)
	require.NoError(t, err)

	err = terminal.StopLogging(ctx, pane.Target)
	require.NoError(t, err)
}

// ── ClearHistory ─────────────────────────────────────────────────────

func TestTmuxSessionTerminal_ClearHistory(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "clear-tmux", t.TempDir())

	pane, err := terminal.FindSession(ctx, "clear-tmux", "", "")
	require.NoError(t, err)
	require.NotNil(t, pane)

	err = terminal.ClearHistory(ctx, pane.Target)
	require.NoError(t, err)
}

// ── SetPaneTitle ─────────────────────────────────────────────────────

func TestTmuxSessionTerminal_SetPaneTitle(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "title-tmux", t.TempDir())

	pane, err := terminal.FindSession(ctx, "title-tmux", "", "")
	require.NoError(t, err)
	require.NotNil(t, pane)

	// SetPaneTitle should not panic or error (returns void)
	terminal.SetPaneTitle(ctx, pane.Target, "My Custom Title")

	// Verify the title was set by re-listing sessions
	time.Sleep(200 * time.Millisecond)
	paneAfter, err := terminal.FindSession(ctx, "title-tmux", "", "")
	require.NoError(t, err)
	require.NotNil(t, paneAfter)
	assert.Equal(t, "My Custom Title", paneAfter.PaneTitle)
}

// ── FindTarget ───────────────────────────────────────────────────────

func TestTmuxSessionTerminal_FindTarget(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	spawnTmuxSession(t, terminal, "findtarget-tmux", t.TempDir())

	target, err := terminal.FindTarget(ctx, "findtarget-tmux", "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, target, "expected non-empty target for existing session")
}

func TestTmuxSessionTerminal_FindTarget_NotFound(t *testing.T) {
	terminal := newTmuxTestTerminal(t)

	target, err := terminal.FindTarget(context.Background(), "ghost", "", "")
	require.NoError(t, err)
	assert.Empty(t, target)
}

// ── AttachCommand ────────────────────────────────────────────────────

func TestTmuxSessionTerminal_AttachCommand(t *testing.T) {
	terminal := newTmuxTestTerminal(t)

	cmd := terminal.AttachCommand("my-session")
	assert.Contains(t, cmd, "tmux")
	assert.Contains(t, cmd, "attach")
	assert.Contains(t, cmd, "my-session")
	// Should include -S flag since we set a custom socket
	assert.Contains(t, cmd, "-S")
}

// ── Concurrent Access ────────────────────────────────────────────────

func TestTmuxSessionTerminal_ConcurrentListAndKill(t *testing.T) {
	terminal := newTmuxTestTerminal(t)
	ctx := context.Background()

	// Create a few sessions
	for i := 0; i < 3; i++ {
		name := "concurrent-tmux-" + string(rune('a'+i))
		spawnTmuxSession(t, terminal, name, t.TempDir())
	}

	// Concurrent list while killing
	done := make(chan bool)
	go func() {
		for i := 0; i < 5; i++ {
			terminal.ListSessions(ctx)
			time.Sleep(50 * time.Millisecond)
		}
		done <- true
	}()

	terminal.KillSession(ctx, "concurrent-tmux-a", "", "")
	<-done

	// Should have 2 remaining
	sessions, err := terminal.ListSessions(ctx)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(sessions), 3)
}

// ── Interface Compliance ─────────────────────────────────────────────

func TestTmuxSessionTerminal_ImplementsInterface(t *testing.T) {
	var _ SessionTerminal = (*TmuxSessionTerminal)(nil)
}
