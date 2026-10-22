package ptymanager

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/subgentic/coral/internal/terminalinput"
)

func canFork(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PTY backend test harness uses creack/pty + sh (unix); Windows uses ConPTY, covered separately (#3629)")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	// Can a PTY be started here at all? Probe the way Coral spawns: a failure that is not
	// a real sandbox fails the test (#3229).
	cmd := exec.Command("true")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		skipIfPTYSpawnDenied(t, err)
		t.Fatalf("pty.StartWithSize: %v", err)
	}
	ptmx.Close()
	cmd.Wait()
}

func TestPTYBackendSpawnAndKill(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	err := backend.Spawn("test-agent", "test", t.TempDir(), "sid-123", "sh -c 'echo hello; sleep 60'", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	if !backend.IsRunning("test-agent") {
		t.Error("expected session to be running")
	}

	sessions := backend.ListSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].AgentName != "test-agent" {
		t.Errorf("expected agent name 'test-agent', got %q", sessions[0].AgentName)
	}

	err = backend.Kill("test-agent")
	if err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	if backend.IsRunning("test-agent") {
		t.Error("expected session to not be running after kill")
	}
}

func TestPTYBackendSendInputAndRead(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	// Use cat which echoes input
	err := backend.Spawn("echo-test", "test", t.TempDir(), "sid-echo", "cat", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	ch, err := backend.Attach("echo-test", "ws-1")
	if err != nil {
		t.Fatalf("Attach failed: %v", err)
	}

	// Send input
	err = backend.SendInput(terminalinput.Unclassified, "echo-test", []byte("hello\n"))
	if err != nil {
		t.Fatalf("SendInput failed: %v", err)
	}

	// Read output (should see "hello" echoed back)
	var output strings.Builder
	timeout := time.After(3 * time.Second)
	for {
		select {
		case data, ok := <-ch:
			if !ok {
				goto done
			}
			output.Write(data)
			if strings.Contains(output.String(), "hello") {
				goto done
			}
		case <-timeout:
			t.Fatalf("timeout waiting for output, got: %q", output.String())
		}
	}
done:

	if !strings.Contains(output.String(), "hello") {
		t.Errorf("expected output to contain 'hello', got %q", output.String())
	}

	backend.Unsubscribe("echo-test", "ws-1")
	backend.Kill("echo-test")
}

func TestPTYBackendResize(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	err := backend.Spawn("resize-test", "test", t.TempDir(), "sid-resize", "sh", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	// The size it was spawned with is reported (#3422).
	if c, r, err := backend.PaneSize("resize-test"); err != nil || c != 80 || r != 24 {
		t.Errorf("PaneSize after spawn = %d x %d, %v; want 80 x 24", c, r, err)
	}

	// Resize should not error
	err = backend.Resize("resize-test", 120, 40)
	if err != nil {
		t.Errorf("Resize failed: %v", err)
	}
	if c, r, err := backend.PaneSize("resize-test"); err != nil || c != 120 || r != 40 {
		t.Errorf("PaneSize after resize = %d x %d, %v; want 120 x 40", c, r, err)
	}

	backend.Kill("resize-test")
	if _, _, err := backend.PaneSize("resize-test"); err == nil {
		t.Error("PaneSize of a killed session: want an error")
	}
}

func TestPTYBackendReplay(t *testing.T) {
	t.Parallel()
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	// the command stays alive after printing: on macOS a process that exits before the first read can lose its output on a loaded machine (#5671)
	err := backend.Spawn("capture-test", "test", t.TempDir(), "sid-cap", "sh -c 'echo MARKER_123; sleep 5'", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	pollFor(func() string { d, _ := backend.Replay("capture-test"); return string(d) }, "MARKER_123")

	data, err := backend.Replay("capture-test")
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}

	if !strings.Contains(string(data), "MARKER_123") {
		t.Errorf("expected replay to contain 'MARKER_123', got %q", string(data))
	}

	backend.Kill("capture-test")
}

func TestPTYBackendLogPath(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	err := backend.Spawn("log-test", "claude", t.TempDir(), "sid-log", "echo test", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	logPath := backend.LogPath("log-test")
	if logPath == "" {
		t.Error("expected non-empty log path")
	}
	if !strings.Contains(logPath, "claude_coral_sid-log.log") {
		t.Errorf("unexpected log path: %q", logPath)
	}

	backend.Kill("log-test")
}

func TestPTYBackendDuplicateSpawn(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	err := backend.Spawn("dup-test", "test", t.TempDir(), "sid-dup", "sh", 80, 24)
	if err != nil {
		t.Fatalf("first Spawn failed: %v", err)
	}

	err = backend.Spawn("dup-test", "test", t.TempDir(), "sid-dup2", "sh", 80, 24)
	if err == nil {
		t.Error("expected error on duplicate spawn")
	}

	backend.Kill("dup-test")
}

func TestPTYBackendNotFound(t *testing.T) {
	backend := NewPTYBackend()

	err := backend.Kill("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}

	err = backend.SendInput(terminalinput.Unclassified, "nonexistent", []byte("test"))
	if err == nil {
		t.Error("expected error for nonexistent session")
	}

	_, err = backend.Replay("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}

	if backend.IsRunning("nonexistent") {
		t.Error("expected false for nonexistent session")
	}
}

func TestPTYBackendRestart(t *testing.T) {
	t.Parallel()
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	// Spawn initial session
	err := backend.Spawn("restart-test", "test", t.TempDir(), "sid-restart", "sh -c 'echo BEFORE; sleep 60'", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	pollFor(func() string { d, _ := backend.Replay("restart-test"); return string(d) }, "BEFORE")

	// Capture initial output
	data, err := backend.Replay("restart-test")
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "BEFORE") {
		t.Logf("initial capture: %q", content)
	}

	// Restart with new command
	err = backend.Restart("restart-test", "sh -c 'echo AFTER; sleep 60'")
	if err != nil {
		t.Fatalf("Restart failed: %v", err)
	}

	// The new command's output, waited for rather than slept for (#4870: 300 ms was too little on a loaded machine).
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err = backend.Replay("restart-test")
		if err != nil {
			t.Fatalf("Replay after restart failed: %v", err)
		}
		content = string(data)
		if strings.Contains(content, "AFTER") || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(content, "AFTER") {
		t.Errorf("expected capture to contain 'AFTER' after restart, got %q", content)
	}
	// Session should still be running
	if !backend.IsRunning("restart-test") {
		t.Error("expected session to be running after restart")
	}

	backend.Kill("restart-test")
}

func TestPTYBackendRestartNotFound(t *testing.T) {
	backend := NewPTYBackend()

	err := backend.Restart("nonexistent", "echo test")
	if err == nil {
		t.Error("expected error for restarting nonexistent session")
	}
}

func TestPTYBackendAttachUnsubscribe(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	err := backend.Spawn("sub-test", "test", t.TempDir(), "sid-sub", "cat", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	// Subscribe
	ch1, err := backend.Attach("sub-test", "ws-1")
	if err != nil {
		t.Fatalf("Attach ws-1 failed: %v", err)
	}
	ch2, err := backend.Attach("sub-test", "ws-2")
	if err != nil {
		t.Fatalf("Attach ws-2 failed: %v", err)
	}

	// Send input
	backend.SendInput(terminalinput.Unclassified, "sub-test", []byte("fan-out-test\n"))

	// Both subscribers should receive output. Read until the marker has come: a pty shows the typed line twice (the terminal's echo, then cat's own output), in chunks that can arrive far apart on a loaded
	// machine, and a read that returns after the first chunk leaves the second in the channel, to be taken for the next message (#6125-era CI: "expected after-unsub, got fan-out-test").
	got1 := readUntil(ch1, "fan-out-test", 2*time.Second)
	got2 := readUntil(ch2, "fan-out-test", 2*time.Second)

	if !strings.Contains(got1, "fan-out-test") {
		t.Errorf("subscriber 1 expected 'fan-out-test', got %q", got1)
	}
	if !strings.Contains(got2, "fan-out-test") {
		t.Errorf("subscriber 2 expected 'fan-out-test', got %q", got2)
	}

	// Unsubscribe one
	backend.Unsubscribe("sub-test", "ws-1")

	// Send more input — only ws-2 should receive
	backend.SendInput(terminalinput.Unclassified, "sub-test", []byte("after-unsub\n"))

	got2 = readUntil(ch2, "after-unsub", 2*time.Second) // skipping whatever cat's output of the first line left behind
	if !strings.Contains(got2, "after-unsub") {
		t.Errorf("subscriber 2 expected 'after-unsub' after ws-1 unsubscribed, got %q", got2)
	}

	backend.Unsubscribe("sub-test", "ws-2")
	backend.Kill("sub-test")
}

func TestPTYBackendAttachNotFound(t *testing.T) {
	backend := NewPTYBackend()

	_, err := backend.Attach("nonexistent", "ws-1")
	if err == nil {
		t.Error("expected error subscribing to nonexistent session")
	}
}

func TestPTYBackendClose(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()

	// Spawn multiple sessions
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("close-test-%d", i)
		err := backend.Spawn(name, "test", t.TempDir(), fmt.Sprintf("sid-close-%d", i), "sleep 60", 80, 24)
		if err != nil {
			t.Fatalf("Spawn %s failed: %v", name, err)
		}
	}

	if len(backend.ListSessions()) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(backend.ListSessions()))
	}

	// Close should kill all sessions
	err := backend.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// All sessions should be gone
	if len(backend.ListSessions()) != 0 {
		t.Errorf("expected 0 sessions after Close, got %d", len(backend.ListSessions()))
	}
}

func TestPTYBackendListSessionsMultiple(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	names := []string{"list-a", "list-b", "list-c"}
	for i, name := range names {
		err := backend.Spawn(name, "claude", t.TempDir(), fmt.Sprintf("sid-%d", i), "sleep 60", 80, 24)
		if err != nil {
			t.Fatalf("Spawn %s failed: %v", name, err)
		}
	}

	sessions := backend.ListSessions()
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	// Verify all sessions are present
	found := map[string]bool{}
	for _, s := range sessions {
		found[s.AgentName] = true
		if s.AgentType != "claude" {
			t.Errorf("expected agent type 'claude', got %q", s.AgentType)
		}
		if !s.Running {
			t.Errorf("expected session %s to be running", s.AgentName)
		}
	}
	for _, name := range names {
		if !found[name] {
			t.Errorf("session %s not found in list", name)
		}
	}
}

func TestPTYBackendConcurrentSpawnKill(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	const n = 5
	errs := make(chan error, n*2)

	// Spawn N sessions concurrently
	for i := 0; i < n; i++ {
		go func(i int) {
			name := fmt.Sprintf("concurrent-%d", i)
			errs <- backend.Spawn(name, "test", t.TempDir(), fmt.Sprintf("sid-c-%d", i), "sleep 60", 80, 24)
		}(i)
	}

	// Wait for all spawns
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Spawn failed: %v", err)
		}
	}

	sessions := backend.ListSessions()
	if len(sessions) != n {
		t.Errorf("expected %d sessions, got %d", n, len(sessions))
	}

	// Kill N sessions concurrently
	for i := 0; i < n; i++ {
		go func(i int) {
			name := fmt.Sprintf("concurrent-%d", i)
			errs <- backend.Kill(name)
		}(i)
	}

	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Kill failed: %v", err)
		}
	}

	sessions = backend.ListSessions()
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions after concurrent kill, got %d", len(sessions))
	}
}

func TestPTYBackendRingBufferOverflow(t *testing.T) {
	t.Parallel()
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	// Spawn a session that produces a lot of output
	err := backend.Spawn("overflow-test", "test", t.TempDir(), "sid-overflow",
		"sh -c 'for i in $(seq 1 5000); do echo LINE_$i; done; sleep 1'", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	// Wait for the last line, not for a guessed time
	pollFor(func() string { d, _ := backend.Replay("overflow-test"); return string(d) }, "LINE_5000")

	// Replay should not error even with large output (ring buffer handles overflow)
	data, err := backend.Replay("overflow-test")
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	content := string(data)

	// Should have some content (ring buffer may have dropped oldest lines)
	if len(content) == 0 {
		t.Error("expected non-empty capture after overflow")
	}

	// The latest lines should be present (ring buffer keeps tail)
	if !strings.Contains(content, "LINE_5000") {
		t.Logf("capture length: %d bytes", len(content))
		// The ring buffer may truncate, so just verify we got some recent content
		if !strings.Contains(content, "LINE_49") {
			t.Errorf("expected recent lines in capture after overflow")
		}
	}

	backend.Kill("overflow-test")
}

func TestPTYBackendAttachFanOut(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()

	err := backend.Spawn("fanout-test", "test", t.TempDir(), "sid-fanout", "cat", 80, 24)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	// Subscribe 5 subscribers
	const numSubs = 5
	channels := make([]<-chan []byte, numSubs)
	for i := 0; i < numSubs; i++ {
		ch, err := backend.Attach("fanout-test", fmt.Sprintf("ws-%d", i))
		if err != nil {
			t.Fatalf("Attach ws-%d failed: %v", i, err)
		}
		channels[i] = ch
	}

	// Send input
	backend.SendInput(terminalinput.Unclassified, "fanout-test", []byte("broadcast\n"))

	// All subscribers should receive
	for i, ch := range channels {
		got := readUntil(ch, "broadcast", 2*time.Second) // until the marker has come: the first chunk may not hold it all on a loaded machine
		if !strings.Contains(got, "broadcast") {
			t.Errorf("subscriber %d expected 'broadcast', got %q", i, got)
		}
	}

	// Cleanup
	for i := 0; i < numSubs; i++ {
		backend.Unsubscribe("fanout-test", fmt.Sprintf("ws-%d", i))
	}
	backend.Kill("fanout-test")
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"echo hello", []string{"echo", "hello"}},
		{"sh -c 'echo test'", shellWrap("sh -c 'echo test'")}, // quotes go to the shell (#4013), never split on spaces
		{"ls | grep foo", shellWrap("ls | grep foo")},         // a metachar wraps via the platform shell (sh -c / powershell)
		{"claude --session-id abc", []string{"claude", "--session-id", "abc"}},
		{"", nil},
	}

	for _, tt := range tests {
		got := parseCommand(tt.input)
		if tt.want == nil && got != nil {
			t.Errorf("parseCommand(%q) = %v, want nil", tt.input, got)
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("parseCommand(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
	}
}

// readUntil collects chunks from ch until the accumulated text holds marker (or the channel closes, or timeout): the output of an earlier line that arrives late is read past, not returned in place of
// the next one.
func readUntil(ch <-chan []byte, marker string, timeout time.Duration) string {
	var buf strings.Builder
	deadline := time.After(timeout)
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

// The race that failed CI: the first line's output comes in two chunks, the second late, and the next line's output comes after that. readUntil reads past the late chunk to the marker it was asked for;
// a read that returns after one chunk (and a short wait for one more) hands back the late chunk of the FIRST line as if it were the next one.
func TestReadUntilReadsPastAnEarlierLinesLateOutput(t *testing.T) {
	ch := make(chan []byte, 8)
	ch <- []byte("fan-out-test\r\n") // the terminal's echo
	go func() {
		time.Sleep(350 * time.Millisecond)
		ch <- []byte("fan-out-test\r\n") // cat's own output of the same line, late
		time.Sleep(50 * time.Millisecond)
		ch <- []byte("after-unsub\r\n")
	}()
	if got := readUntil(ch, "fan-out-test", time.Second); !strings.Contains(got, "fan-out-test") {
		t.Fatalf("the first line: %q", got)
	}
	if got := readUntil(ch, "after-unsub", 2*time.Second); !strings.Contains(got, "after-unsub") {
		t.Fatalf("readUntil should read past the first line's late chunk to the next line: %q", got)
	}
}
