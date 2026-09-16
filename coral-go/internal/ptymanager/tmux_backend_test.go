package ptymanager

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/tmux"
)

// newTestTmuxBackend creates a TmuxBackend with an isolated tmux socket
// and logDir. Cleans up the tmux server on test completion.
func newTestTmuxBackend(t *testing.T) *TmuxBackend {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	sock := shortSocketPath(t)

	// Smoke-test that tmux can actually create a detached session on this
	// socket in this environment. Some CI sandboxes reject new-session with
	// exit 1 (no TTY, missing TERM, seccomp, etc.) — skip rather than fail.
	if out, err := exec.Command("tmux", "-S", sock, "new-session", "-d", "-s", "coral-smoke", "sleep 1").CombinedOutput(); err != nil {
		exec.Command("tmux", "-S", sock, "kill-server").Run()
		t.Skipf("tmux cannot create sessions in this environment: %v (output: %s)", err, strings.TrimSpace(string(out)))
	}
	exec.Command("tmux", "-S", sock, "kill-server").Run()

	client := tmux.NewClient()
	client.SocketPath = sock
	client.FallbackToDefault = false

	logDir := filepath.Join(t.TempDir(), "logs")
	os.MkdirAll(logDir, 0755)

	backend := NewTmuxBackend(client, logDir)

	t.Cleanup(func() {
		exec.Command("tmux", "-S", sock, "kill-server").Run()
	})

	return backend
}

// shortSocketPath returns a path under os.TempDir short enough to fit within
// the Unix domain socket path limit (104 bytes on macOS/darwin, 108 on Linux).
// t.TempDir() paths include the full test name and can exceed this limit,
// causing tmux new-session to fail with "File name too long".
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "coral-tmux-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "t.sock")
}

func TestTmuxBackend_SpawnAndKill(t *testing.T) {
	b := newTestTmuxBackend(t)

	err := b.Spawn("test-agent", "claude", t.TempDir(), "sid-001", "sleep 60", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	if !b.IsRunning("test-agent") {
		t.Error("expected session to be running after spawn")
	}

	pid, err := b.SessionPID(context.Background(), "test-agent")
	if err != nil {
		t.Fatalf("SessionPID failed: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("SessionPID = %d, want a live pane PID", pid)
	}

	err = b.Kill("test-agent")
	if err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	if b.IsRunning("test-agent") {
		t.Error("expected session to not be running after kill")
	}
}

func TestTmuxBackend_SpawnCreatesLogFile(t *testing.T) {
	b := newTestTmuxBackend(t)

	err := b.Spawn("log-test", "claude", t.TempDir(), "sid-log", "sleep 60", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer b.Kill("log-test")

	logPath := b.LogPath("log-test")
	if logPath == "" {
		t.Fatal("expected non-empty log path")
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("log file does not exist: %v", err)
	}
}

func TestTmuxBackend_Replay(t *testing.T) {
	b := newTestTmuxBackend(t)

	err := b.Spawn("replay-test", "claude", t.TempDir(), "sid-cap", "sh -c 'echo MARKER_REPLAY_42'", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer b.Kill("replay-test")

	var content string
	for i := 0; i < 20; i++ {
		time.Sleep(200 * time.Millisecond)
		data, _ := b.Replay("replay-test")
		content = string(data)
		if strings.Contains(content, "MARKER_REPLAY_42") {
			return // success
		}
	}
	t.Errorf("expected replay to contain MARKER_REPLAY_42, got: %q", content)
}

func TestTmuxBackend_ReplayUsesCoherentPaneSnapshot(t *testing.T) {
	b := newTestTmuxBackend(t)

	// The script overwrites a line in place. A raw-log replay contains both
	// values, while a coherent capture of tmux's grid contains only the final
	// rendered value. Keep the stale text out of the shell command itself.
	dir := t.TempDir()
	script := filepath.Join(dir, "redraw.sh")
	if err := os.WriteFile(script, []byte("printf 'STALE_COMMAND_TEXT\\r\\033[2KFINAL_SNAPSHOT_TEXT\\n'\nsleep 60\n"), 0755); err != nil {
		t.Fatalf("write redraw script: %v", err)
	}
	err := b.Spawn("snapshot-test", "claude", dir, "sid-snapshot",
		"sh "+script, 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer b.Kill("snapshot-test")

	var content string
	for i := 0; i < 20; i++ {
		time.Sleep(200 * time.Millisecond)
		data, _ := b.Replay("snapshot-test")
		content = string(data)
		if strings.Contains(content, "FINAL_SNAPSHOT_TEXT") {
			break
		}
	}
	if !strings.Contains(content, "FINAL_SNAPSHOT_TEXT") {
		t.Fatalf("expected replay to contain final screen, got: %q", content)
	}
	if strings.Contains(content, "STALE_COMMAND_TEXT") {
		t.Fatalf("replay retained overwritten text from the raw event stream: %q", content)
	}
	if strings.Contains(strings.ReplaceAll(content, "\r\n", ""), "\n") {
		t.Fatalf("replay contains bare LF that would produce staircase rendering: %q", content)
	}
}

func TestNormalizeCaptureForXterm(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "bare LF", in: "one\ntwo\n", want: "one\r\ntwo\r\n"},
		{name: "existing CRLF", in: "one\r\ntwo\r\n", want: "one\r\ntwo\r\n"},
		{name: "mixed", in: "one\r\ntwo\nthree", want: "one\r\ntwo\r\nthree"},
		{name: "no newline", in: "one", want: "one"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeCaptureForXterm(tt.in); got != tt.want {
				t.Fatalf("normalizeCaptureForXterm(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTmuxBackend_SendInput(t *testing.T) {
	b := newTestTmuxBackend(t)

	err := b.Spawn("input-test", "claude", t.TempDir(), "sid-inp", "cat", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer b.Kill("input-test")

	time.Sleep(500 * time.Millisecond)

	err = b.SendInput("input-test", []byte("echo SEND_TEST_MARKER\n"))
	if err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}

	var content string
	for i := 0; i < 20; i++ {
		time.Sleep(200 * time.Millisecond)
		data, _ := b.Replay("input-test")
		content = string(data)
		if strings.Contains(content, "SEND_TEST_MARKER") {
			return
		}
	}
	t.Errorf("expected replay to contain SEND_TEST_MARKER, got: %q", content)
}

func TestTmuxBackend_ListSessions(t *testing.T) {
	b := newTestTmuxBackend(t)

	// Use UUID-formatted session IDs so ParseSessionName can extract them
	sidA := "aaaaaaaa-1111-2222-3333-444444444444"
	sidB := "bbbbbbbb-1111-2222-3333-444444444444"

	err := b.Spawn("agent1", "claude", t.TempDir(), sidA, "sleep 60", 80, 24)
	if err != nil {
		t.Fatalf("Spawn 1 failed: %v", err)
	}
	defer b.Kill("agent1")

	err = b.Spawn("agent2", "gemini", t.TempDir(), sidB, "sleep 60", 80, 24)
	if err != nil {
		t.Fatalf("Spawn 2 failed: %v", err)
	}
	defer b.Kill("agent2")

	time.Sleep(500 * time.Millisecond)

	sessions := b.ListSessions()
	if len(sessions) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(sessions))
	}

	found := map[string]bool{}
	for _, s := range sessions {
		found[s.SessionID] = true
	}
	if !found[sidA] {
		t.Errorf("expected to find session %s", sidA)
	}
	if !found[sidB] {
		t.Errorf("expected to find session %s", sidB)
	}
}

func TestTmuxBackend_Restart(t *testing.T) {
	b := newTestTmuxBackend(t)

	err := b.Spawn("restart-test", "claude", t.TempDir(), "sid-rst", "sleep 60", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer b.Kill("restart-test")

	time.Sleep(500 * time.Millisecond)

	err = b.Restart("restart-test", "echo RESTARTED_MARKER")
	if err != nil {
		t.Fatalf("Restart failed: %v", err)
	}

	var content string
	for i := 0; i < 20; i++ {
		time.Sleep(200 * time.Millisecond)
		data, _ := b.Replay("restart-test")
		content = string(data)
		if strings.Contains(content, "RESTARTED_MARKER") {
			return
		}
	}
	t.Errorf("expected capture to contain RESTARTED_MARKER after restart, got: %q", content)
}

func TestTmuxBackend_Resize(t *testing.T) {
	b := newTestTmuxBackend(t)

	err := b.Spawn("resize-test", "claude", t.TempDir(), "sid-rsz", "sleep 60", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer b.Kill("resize-test")

	time.Sleep(300 * time.Millisecond)

	err = b.Resize("resize-test", 120, 40)
	if err != nil {
		t.Errorf("Resize failed: %v", err)
	}
}

func TestTmuxBackend_LogPath(t *testing.T) {
	b := newTestTmuxBackend(t)

	// Unknown session returns empty
	if got := b.LogPath("nonexistent"); got != "" {
		t.Errorf("expected empty log path for unknown session, got %q", got)
	}

	err := b.Spawn("logpath-test", "claude", t.TempDir(), "sid-lp", "sleep 60", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer b.Kill("logpath-test")

	logPath := b.LogPath("logpath-test")
	if !strings.Contains(logPath, "claude_coral_sid-lp.log") {
		t.Errorf("unexpected log path: %q", logPath)
	}
}

func TestTmuxBackend_KillNotFound(t *testing.T) {
	b := newTestTmuxBackend(t)

	// Killing a nonexistent session should not panic (may return error)
	_ = b.Kill("nonexistent-session")
}

func TestTmuxBackend_ReplayNotFound(t *testing.T) {
	b := newTestTmuxBackend(t)

	data, err := b.Replay("nonexistent-session")
	if err == nil {
		t.Errorf("expected error replaying unknown session, got %d bytes", len(data))
	}
}

func TestTmuxBackend_AttachNotFound(t *testing.T) {
	b := newTestTmuxBackend(t)

	ch, err := b.Attach("any", "ws-1")
	if err == nil {
		t.Error("expected error attaching to unknown session")
	}
	if ch != nil {
		t.Error("expected nil channel for unknown session")
	}
}

func TestTmuxBackend_Close(t *testing.T) {
	b := newTestTmuxBackend(t)

	err := b.Spawn("close-test", "claude", t.TempDir(), "sid-cls", "sleep 60", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	// Close should be a no-op (not kill sessions)
	if err := b.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}

	// Session should still be running in tmux after Close
	if !b.IsRunning("close-test") {
		t.Error("expected session to still be running after Close")
	}

	b.Kill("close-test")
}

// TestTmuxBackendFanOut verifies TEST_PLAN §2.2 — two attachers on the same
// session both receive identical live bytes, Unsubscribe isolates one without
// affecting the other, and no tail goroutines leak after both unsubscribe.
func TestTmuxBackendFanOut(t *testing.T) {
	b := newTestTmuxBackend(t)

	err := b.Spawn("fanout", "claude", t.TempDir(), "sid-fan", "cat", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer b.Kill("fanout")
	time.Sleep(300 * time.Millisecond)

	chA, err := b.Attach("fanout", "subA")
	if err != nil {
		t.Fatalf("Attach A failed: %v", err)
	}
	chB, err := b.Attach("fanout", "subB")
	if err != nil {
		t.Fatalf("Attach B failed: %v", err)
	}
	if chA == nil || chB == nil {
		t.Fatal("expected non-nil channels for live session")
	}
	if chA == chB {
		t.Fatal("expected distinct channels for distinct subscribers")
	}

	// Trigger output — cat echoes input
	if err := b.SendInput("fanout", []byte("FANOUT_MARKER\n")); err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}

	readUntil := func(ch <-chan []byte, marker string, timeout time.Duration) string {
		deadline := time.After(timeout)
		var buf strings.Builder
		for {
			select {
			case data, ok := <-ch:
				if !ok {
					return buf.String()
				}
				buf.Write(data)
				if strings.Contains(buf.String(), marker) {
					return buf.String()
				}
			case <-deadline:
				return buf.String()
			}
		}
	}

	gotA := readUntil(chA, "FANOUT_MARKER", 3*time.Second)
	gotB := readUntil(chB, "FANOUT_MARKER", 3*time.Second)
	if !strings.Contains(gotA, "FANOUT_MARKER") {
		t.Errorf("subscriber A missed FANOUT_MARKER, got %q", gotA)
	}
	if !strings.Contains(gotB, "FANOUT_MARKER") {
		t.Errorf("subscriber B missed FANOUT_MARKER, got %q", gotB)
	}

	// Unsubscribe A; confirm B still receives.
	b.Unsubscribe("fanout", "subA")
	if err := b.SendInput("fanout", []byte("SECOND_MARKER\n")); err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}
	gotB2 := readUntil(chB, "SECOND_MARKER", 3*time.Second)
	if !strings.Contains(gotB2, "SECOND_MARKER") {
		t.Errorf("subscriber B lost delivery after A unsubscribed, got %q", gotB2)
	}

	// Unsubscribe B; tail should be torn down.
	b.Unsubscribe("fanout", "subB")

	b.mu.RLock()
	_, tailStillRunning := b.tails["fanout"]
	b.mu.RUnlock()
	if tailStillRunning {
		t.Error("expected tail goroutine to be torn down after last unsubscribe")
	}
}

// TestTmuxBackendAttachBytePreservation verifies TEST_PLAN §5.1 — all 256 byte
// values written to the pipe-pane log reach the Attach subscriber channel
// byte-identical. This catches any accidental `string(data)` coercion on the
// server side that would silently substitute invalid bytes with U+FFFD. The
// test writes directly to the log file because a live shell would interpret
// control chars (0x03 EOF, 0x1a SIGTSTP, etc.), which would mask the wire-level
// property we want to verify: the log→tail→subscriber path is byte-transparent.
func TestTmuxBackendAttachBytePreservation(t *testing.T) {
	b := newTestTmuxBackend(t)

	if err := b.Spawn("bytes", "claude", t.TempDir(), "sid-bytes", "sleep 60", 80, 24); err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer b.Kill("bytes")
	time.Sleep(300 * time.Millisecond)

	ch, err := b.Attach("bytes", "sub-bytes")
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	defer b.Unsubscribe("bytes", "sub-bytes")

	// Build a payload covering the full byte range 0x00..0xff plus a known
	// ANSI escape — `cat -v` would render these as caret notation, but we
	// bypass the shell and write to the log directly.
	payload := make([]byte, 0, 256+len("\x1b[31mRED\x1b[0m"))
	for i := 0; i < 256; i++ {
		payload = append(payload, byte(i))
	}
	payload = append(payload, []byte("\x1b[31mRED\x1b[0m")...)

	logPath := b.LogPath("bytes")
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		t.Fatalf("write log: %v", err)
	}
	f.Close()

	// Accumulate bytes from the subscriber channel. fsnotify may deliver the
	// payload in multiple chunks, so keep reading until we have enough bytes
	// or time out.
	received := make([]byte, 0, len(payload))
	deadline := time.After(5 * time.Second)
	for len(received) < len(payload) {
		select {
		case data, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed prematurely, got %d/%d bytes", len(received), len(payload))
			}
			received = append(received, data...)
		case <-deadline:
			t.Fatalf("timed out: received %d/%d bytes", len(received), len(payload))
		}
	}

	if len(received) < len(payload) {
		t.Fatalf("short read: got %d bytes, want %d", len(received), len(payload))
	}
	// The first len(payload) bytes must match exactly. Any trailing bytes are
	// shell-prompt noise from the sleeping pane and are ignored.
	if !bytesEqual(received[:len(payload)], payload) {
		for i := 0; i < len(payload); i++ {
			if received[i] != payload[i] {
				t.Fatalf("byte %d differs: got 0x%02x, want 0x%02x", i, received[i], payload[i])
			}
		}
	}
}

// TestTmuxBackendAttachPartialUTF8Boundary verifies TEST_PLAN §5.2 — a UTF-8
// codepoint split across two separate writes to the pipe-pane log arrives on
// the Attach subscriber channel in order and reassembles to the original bytes.
// This guards against any code path that might try to decode partial UTF-8 and
// drop the boundary bytes.
func TestTmuxBackendAttachPartialUTF8Boundary(t *testing.T) {
	b := newTestTmuxBackend(t)

	if err := b.Spawn("utf8", "claude", t.TempDir(), "sid-utf8", "sleep 60", 80, 24); err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer b.Kill("utf8")
	time.Sleep(300 * time.Millisecond)

	ch, err := b.Attach("utf8", "sub-utf8")
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}
	defer b.Unsubscribe("utf8", "sub-utf8")

	// "✅" (U+2705) encodes as 3 bytes: 0xe2 0x9c 0x85. Split across writes so
	// the first write ends mid-codepoint. Emit a second full "✅" afterwards so
	// we can observe two complete emojis reassembled in order.
	first := []byte{0xe2, 0x9c}
	second := []byte{0x85}
	third := []byte("✅")
	expected := append(append(append([]byte{}, first...), second...), third...)

	logPath := b.LogPath("utf8")
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()

	if _, err := f.Write(first); err != nil {
		t.Fatalf("write first: %v", err)
	}
	// Small gap so fsnotify delivers the first chunk before the rest.
	time.Sleep(50 * time.Millisecond)
	if _, err := f.Write(second); err != nil {
		t.Fatalf("write second: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := f.Write(third); err != nil {
		t.Fatalf("write third: %v", err)
	}

	received := make([]byte, 0, len(expected))
	deadline := time.After(5 * time.Second)
	for len(received) < len(expected) {
		select {
		case data, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed prematurely, got %d bytes", len(received))
			}
			received = append(received, data...)
		case <-deadline:
			t.Fatalf("timed out: received %d/%d bytes", len(received), len(expected))
		}
	}

	if !bytesEqual(received[:len(expected)], expected) {
		t.Fatalf("bytes differ: got %x, want %x", received[:len(expected)], expected)
	}
	// Sanity: the received bytes should decode to two valid emoji when
	// concatenated. If the boundary byte was dropped, this would be garbage.
	got := string(received[:len(expected)])
	if !strings.Contains(got, "✅✅") {
		t.Fatalf("expected concatenated bytes to decode to \"✅✅\", got %q", got)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTmuxBackend_ConcurrentSpawnKill(t *testing.T) {
	b := newTestTmuxBackend(t)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := strings.Replace("conc-IDX", "IDX", string(rune('a'+idx)), 1)
			sid := strings.Replace("sid-conc-IDX", "IDX", string(rune('0'+idx)), 1)
			b.Spawn(name, "claude", t.TempDir(), sid, "sleep 60", 80, 24)
		}(i)
	}
	wg.Wait()

	time.Sleep(500 * time.Millisecond)

	var wg2 sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg2.Add(1)
		go func(idx int) {
			defer wg2.Done()
			name := strings.Replace("conc-IDX", "IDX", string(rune('a'+idx)), 1)
			b.Kill(name)
		}(i)
	}
	wg2.Wait()
}
