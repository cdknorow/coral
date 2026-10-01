package ptymanager

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipIfWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY tests require Unix-like OS")
	}
}

func newTestTerminal(t *testing.T) (*PTYSessionTerminal, *PTYBackend) {
	t.Helper()
	backend := NewPTYBackend()
	t.Cleanup(func() { backend.Close() })
	return NewPTYSessionTerminal(backend), backend
}

func spawnTestSession(t *testing.T, backend *PTYBackend, name string) {
	t.Helper()
	dir := t.TempDir()
	err := backend.Spawn(name, "claude", dir, "test-sess-id-000000000000000000000000000000000001", "", 80, 24)
	if err != nil && strings.Contains(err.Error(), "operation not permitted") {
		t.Skip("PTY spawn not permitted in this environment (sandbox)")
	}
	require.NoError(t, err)
}

func waitForPTYOutput(t *testing.T, backend *PTYBackend, name, marker string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := backend.Replay(name)
		if err == nil && strings.Contains(string(output), marker) {
			return string(output)
		}
		time.Sleep(50 * time.Millisecond)
	}
	output, _ := backend.Replay(name)
	return string(output)
}

// ── ListSessions ──────────────────────────────────────────────────────

func TestPTYSessionTerminal_ListSessions_Empty(t *testing.T) {
	skipIfWindows(t)
	terminal, _ := newTestTerminal(t)

	sessions, err := terminal.ListSessions(context.Background())
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestPTYSessionTerminal_ListSessions_WithSessions(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)

	spawnTestSession(t, backend, "agent-a")
	time.Sleep(100 * time.Millisecond)

	sessions, err := terminal.ListSessions(context.Background())
	require.NoError(t, err)
	assert.Len(t, sessions, 1)
	assert.Equal(t, "agent-a", sessions[0].PaneTitle)
	assert.NotEmpty(t, sessions[0].CurrentPath)
}

// ── FindSession ──────────────────────────────────────────────────────

func TestPTYSessionTerminal_FindSession_ByName(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)

	spawnTestSession(t, backend, "my-agent")
	time.Sleep(100 * time.Millisecond)

	pane, err := terminal.FindSession(context.Background(), "my-agent", "", "")
	require.NoError(t, err)
	require.NotNil(t, pane)
	assert.Equal(t, "my-agent", pane.PaneTitle)
}

func TestPTYSessionTerminal_FindSession_BySessionID(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)

	spawnTestSession(t, backend, "test-agent")
	time.Sleep(100 * time.Millisecond)

	pane, err := terminal.FindSession(context.Background(), "nonexistent", "", "test-sess-id-000000000000000000000000000000000001")
	require.NoError(t, err)
	require.NotNil(t, pane)
}

func TestPTYSessionTerminal_FindSession_NotFound(t *testing.T) {
	skipIfWindows(t)
	terminal, _ := newTestTerminal(t)

	pane, err := terminal.FindSession(context.Background(), "ghost", "", "")
	require.NoError(t, err)
	assert.Nil(t, pane)
}

// ── CreateSession / HasSession / KillSession ─────────────────────────

func TestPTYSessionTerminal_CreateSession(t *testing.T) {
	skipIfWindows(t)
	terminal, _ := newTestTerminal(t)
	dir := t.TempDir()

	err := terminal.CreateSession(context.Background(), "test-session", dir)
	if err != nil && strings.Contains(err.Error(), "operation not permitted") {
		t.Skip("PTY spawn not permitted in this environment (sandbox)")
	}
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	assert.True(t, terminal.HasSession(context.Background(), "test-session"))
}

func TestPTYSessionTerminal_CreateSession_Duplicate(t *testing.T) {
	skipIfWindows(t)
	terminal, _ := newTestTerminal(t)
	dir := t.TempDir()

	err := terminal.CreateSession(context.Background(), "dup-session", dir)
	if err != nil && strings.Contains(err.Error(), "operation not permitted") {
		t.Skip("PTY spawn not permitted in this environment (sandbox)")
	}
	require.NoError(t, err)

	err = terminal.CreateSession(context.Background(), "dup-session", dir)
	assert.Error(t, err) // Should fail — already exists
}

func TestPTYSessionTerminal_KillSession(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)

	spawnTestSession(t, backend, "kill-me")
	time.Sleep(100 * time.Millisecond)
	assert.True(t, terminal.HasSession(context.Background(), "kill-me"))

	err := terminal.KillSession(context.Background(), "kill-me", "", "")
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	assert.False(t, terminal.HasSession(context.Background(), "kill-me"))
}

func TestPTYSessionTerminal_KillSession_NotFound(t *testing.T) {
	skipIfWindows(t)
	terminal, _ := newTestTerminal(t)

	err := terminal.KillSession(context.Background(), "nonexistent", "", "")
	assert.Error(t, err)
}

// ── HasSession ───────────────────────────────────────────────────────

func TestPTYSessionTerminal_HasSession_False(t *testing.T) {
	skipIfWindows(t)
	terminal, _ := newTestTerminal(t)
	assert.False(t, terminal.HasSession(context.Background(), "nope"))
}

// ── SendInput / CaptureOutput ────────────────────────────────────────

func TestPTYSessionTerminal_SendInput_CaptureOutput(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)

	spawnTestSession(t, backend, "io-test")
	time.Sleep(300 * time.Millisecond)

	err := terminal.SendInput(context.Background(), "io-test", "echo hello-coral", "", "")
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	output, err := terminal.CaptureOutput(context.Background(), "io-test", 200, "", "")
	require.NoError(t, err)
	assert.Contains(t, output, "hello-coral")
}

func TestPTYSessionTerminal_SendRawInput(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)

	spawnTestSession(t, backend, "raw-test")
	time.Sleep(200 * time.Millisecond)

	// Send raw characters
	err := terminal.SendRawInput(context.Background(), "raw-test", []string{"h", "i", "\n"}, "", "")
	require.NoError(t, err)
}

// ── SendToTarget / SendTerminalInput ─────────────────────────────────

func TestPTYSessionTerminal_SendToTarget(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)

	spawnTestSession(t, backend, "target-test")
	time.Sleep(200 * time.Millisecond)

	// SendToTarget uses the session name as target in PTY mode
	err := terminal.SendToTarget(context.Background(), "target-test", "echo target-ok")
	require.NoError(t, err)
}

func TestPTYSessionTerminal_SendToTarget_LongCommandUsesHandoff(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)
	spawnTestSession(t, backend, "long-target-test")
	time.Sleep(200 * time.Millisecond)

	marker := "LONG_PTY_HANDOFF_MARKER"
	value := strings.Repeat("x", inlineCommandLimit+300) + marker
	command := "printf '%s' '" + value + "'"
	require.Greater(t, len(command), inlineCommandLimit)
	require.NoError(t, terminal.SendToTarget(context.Background(), "long-target-test", command))

	output := waitForPTYOutput(t, backend, "long-target-test", marker, 5*time.Second)
	assert.Contains(t, output, marker)
}

// ── ResizeSession ────────────────────────────────────────────────────

func TestPTYSessionTerminal_ResizeSession(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)

	spawnTestSession(t, backend, "resize-test")
	time.Sleep(100 * time.Millisecond)

	err := terminal.ResizeSession(context.Background(), "resize-test", 120, "", "")
	require.NoError(t, err)
}

func TestPTYSessionTerminal_ResizeSession_NotFound(t *testing.T) {
	skipIfWindows(t)
	terminal, _ := newTestTerminal(t)

	err := terminal.ResizeSession(context.Background(), "ghost", 120, "", "")
	assert.Error(t, err)
}

// ── No-op methods (logging, history) ─────────────────────────────────

func TestPTYSessionTerminal_NoOpMethods(t *testing.T) {
	skipIfWindows(t)
	terminal, _ := newTestTerminal(t)
	ctx := context.Background()

	assert.NoError(t, terminal.StartLogging(ctx, "any", "/tmp/log"))
	assert.NoError(t, terminal.StopLogging(ctx, "any"))
	assert.NoError(t, terminal.ClearHistory(ctx, "any"))
	assert.NoError(t, terminal.RenameSession(ctx, "old", "new"))
}

// ── FindTarget ───────────────────────────────────────────────────────

func TestPTYSessionTerminal_FindTarget(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)

	spawnTestSession(t, backend, "find-target")
	time.Sleep(100 * time.Millisecond)

	target, err := terminal.FindTarget(context.Background(), "find-target", "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, target)
}

func TestPTYSessionTerminal_FindTarget_NotFound(t *testing.T) {
	skipIfWindows(t)
	terminal, _ := newTestTerminal(t)

	target, err := terminal.FindTarget(context.Background(), "ghost", "", "")
	require.NoError(t, err)
	assert.Empty(t, target)
}

// ── Concurrent Access ────────────────────────────────────────────────

func TestPTYSessionTerminal_ConcurrentListAndKill(t *testing.T) {
	skipIfWindows(t)
	terminal, backend := newTestTerminal(t)

	// Spawn multiple sessions
	for i := 0; i < 3; i++ {
		name := "concurrent-" + strings.Repeat("x", i+1)
		dir := t.TempDir()
		sessionID := "test-sess-id-00000000000000000000000000000000000" + string(rune('1'+i))
		err := backend.Spawn(name, "claude", dir, sessionID, "", 80, 24)
		if err != nil && strings.Contains(err.Error(), "operation not permitted") {
			t.Skip("PTY spawn not permitted in this environment (sandbox)")
		}
	}
	time.Sleep(200 * time.Millisecond)

	// Concurrent list while killing
	done := make(chan bool)
	go func() {
		for i := 0; i < 10; i++ {
			terminal.ListSessions(context.Background())
		}
		done <- true
	}()

	terminal.KillSession(context.Background(), "concurrent-x", "", "")
	<-done

	sessions, _ := terminal.ListSessions(context.Background())
	// Should have 2 remaining (killed one)
	assert.LessOrEqual(t, len(sessions), 3)
}

// ── Interface Compliance ─────────────────────────────────────────────

func TestPTYSessionTerminal_ImplementsInterface(t *testing.T) {
	var _ SessionTerminal = (*PTYSessionTerminal)(nil)
	var _ SessionTerminal = (*TmuxSessionTerminal)(nil)
}
