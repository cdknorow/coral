//go:build !windows

package ptymanager

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewSession_CreatesLogFile(t *testing.T) {
	canFork(t)
	s, err := newSession("log-create-test", "claude", t.TempDir(), "sid-log-create", "echo hello", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}
	defer s.kill()

	if s.logPath == "" {
		t.Fatal("expected non-empty logPath")
	}
	if _, err := os.Stat(s.logPath); os.IsNotExist(err) {
		t.Errorf("log file not created at %s", s.logPath)
	}
	if !strings.Contains(s.logPath, "claude_coral_sid-log-create.log") {
		t.Errorf("unexpected log path format: %s", s.logPath)
	}
}

func TestNewSession_SetsEnvironment(t *testing.T) {
	canFork(t)
	s, err := newSession("env-test", "test", t.TempDir(), "sid-env", "env", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}
	defer s.kill()

	// Wait for output
	time.Sleep(500 * time.Millisecond)
	content := string(s.replayBytes())

	if !strings.Contains(content, "TERM=xterm-256color") {
		t.Errorf("expected TERM=xterm-256color in env output, got: %q", content)
	}
}

// An empty command starts the default shell (newSession's documented default); the
// test used to "pass" only because every spawn failed on macOS (#3229).
func TestNewSession_EmptyCommand(t *testing.T) {
	canFork(t)
	s, err := newSession("empty-cmd", "test", t.TempDir(), "sid-empty", "", 80, 24)
	if err != nil {
		t.Fatalf("an empty command should start the default shell: %v", err)
	}
	defer s.kill()
	if !s.isRunning() {
		t.Error("the default shell should be running")
	}
}

func TestSession_KillGraceful(t *testing.T) {
	canFork(t)
	// sleep exits immediately on SIGTERM (graceful)
	s, err := newSession("kill-graceful", "test", t.TempDir(), "sid-graceful", "sleep 60", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}

	if !s.isRunning() {
		t.Fatal("expected session to be running")
	}

	start := time.Now()
	s.kill()
	elapsed := time.Since(start)

	if s.isRunning() {
		t.Error("expected session to not be running after kill")
	}
	// Graceful kill of sleep should complete in under 2s (not hit 5s timeout)
	if elapsed > 3*time.Second {
		t.Errorf("kill took %v, expected graceful termination under 3s", elapsed)
	}
}

func TestSession_KillClosesSubscribers(t *testing.T) {
	canFork(t)
	s, err := newSession("kill-subs", "test", t.TempDir(), "sid-kill-subs", "sleep 60", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}

	ch1 := s.subscribe("sub-1")
	ch2 := s.subscribe("sub-2")

	s.kill()

	// Both channels should be closed
	select {
	case _, ok := <-ch1:
		if ok {
			t.Error("expected ch1 to be closed")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for ch1 to close")
	}

	select {
	case _, ok := <-ch2:
		if ok {
			t.Error("expected ch2 to be closed")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for ch2 to close")
	}
}

func TestSession_KillClosesLogFile(t *testing.T) {
	canFork(t)
	s, err := newSession("kill-log", "test", t.TempDir(), "sid-kill-log", "echo done", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}

	logPath := s.logPath
	s.kill()

	// Log file should exist (not deleted, just closed)
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Error("log file should still exist after kill (just closed)")
	}
}

func TestSession_ReadLoopFansOut(t *testing.T) {
	canFork(t)
	s, err := newSession("fanout-test", "test", t.TempDir(), "sid-fanout", "cat", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}
	defer s.kill()

	ch1 := s.subscribe("sub-1")
	ch2 := s.subscribe("sub-2")

	// Send input
	s.writeFull([]byte("FANOUT_MARKER\n"))

	// Both subscribers should receive the echoed output
	received := make([]bool, 2)
	timeout := time.After(3 * time.Second)

	for i, ch := range []<-chan []byte{ch1, ch2} {
		var buf strings.Builder
		for {
			select {
			case data, ok := <-ch:
				if !ok {
					goto next
				}
				buf.Write(data)
				if strings.Contains(buf.String(), "FANOUT_MARKER") {
					received[i] = true
					goto next
				}
			case <-timeout:
				goto next
			}
		}
	next:
	}

	for i, got := range received {
		if !got {
			t.Errorf("subscriber %d did not receive FANOUT_MARKER", i+1)
		}
	}
}

func TestSession_RingBufferOverflow(t *testing.T) {
	canFork(t)
	s, err := newSession("ring-test", "test", t.TempDir(), "sid-ring", "cat", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}
	defer s.kill()

	// Send more data than ring buffer can hold (256KB)
	bigChunk := strings.Repeat("X", 1024) + "\n"
	for i := 0; i < 300; i++ { // 300KB > 256KB ring
		s.writeFull([]byte(bigChunk))
	}

	time.Sleep(time.Second)

	content := string(s.replayBytes())
	if len(content) > s.ringMax+1024 {
		t.Errorf("ring buffer exceeded max: got %d bytes, max %d", len(content), s.ringMax)
	}
}

func TestSession_ConcurrentSubscribeUnsubscribe(t *testing.T) {
	canFork(t)
	s, err := newSession("concurrent-test", "test", t.TempDir(), "sid-concurrent", "cat", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}
	defer s.kill()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			subID := "sub-" + strings.Repeat("x", id)
			ch := s.subscribe(subID)
			time.Sleep(10 * time.Millisecond)
			s.unsubscribe(subID)
			// Channel should be closed after unsubscribe
			select {
			case <-ch: // data received before the close is fine
			case <-time.After(time.Second):
				// Already closed or no data
			}
		}(i)
	}
	wg.Wait()
}

func TestSession_ProcessExitClosesReadLoop(t *testing.T) {
	canFork(t)
	s, err := newSession("exit-test", "test", t.TempDir(), "sid-exit", "echo bye", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}
	defer s.kill()

	// Process should exit quickly
	select {
	case <-s.proc.Done():
		// Good — process exited
	case <-time.After(3 * time.Second):
		t.Error("process did not exit within timeout")
	}

	if s.isRunning() {
		t.Error("expected isRunning to return false after process exits")
	}
}

func TestSession_WorkingDirectory(t *testing.T) {
	canFork(t)
	tmpDir := t.TempDir()
	s, err := newSession("workdir-test", "test", tmpDir, "sid-workdir", "pwd", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}
	defer s.kill()

	time.Sleep(500 * time.Millisecond)
	content := string(s.replayBytes())

	// Resolve symlinks for comparison (macOS /tmp → /private/tmp)
	resolvedTmp, _ := filepath.EvalSymlinks(tmpDir)
	if !strings.Contains(content, resolvedTmp) && !strings.Contains(content, tmpDir) {
		t.Errorf("expected working directory %q in output, got %q", tmpDir, content)
	}
}

func TestParseCommand_ShellMetachars(t *testing.T) {
	tests := []struct {
		input     string
		wantShell bool
	}{
		{"echo hello", false},
		{"ls | grep foo", true},
		{"echo $HOME", true},
		{"cmd1 && cmd2", true},
		{"echo `date`", true},
		{"echo $(whoami)", true},
		{"echo hello > file.txt", true},
		{"cat < input.txt", true},
		{"ls *.go", true},
		{"echo test!", true},
		{"simple-command --flag value", false},
		// Quotes and a backslash are shell syntax too (#4013).
		{"sh -c 'sleep 60'", true},
		{`echo "a b"`, true},
		{`printf a\ b`, true},
	}

	for _, tt := range tests {
		parts := parseCommand(tt.input)
		if parts == nil {
			t.Errorf("parseCommand(%q) returned nil", tt.input)
			continue
		}
		isShellWrapped := parts[0] == "sh" && parts[1] == "-c"
		if isShellWrapped != tt.wantShell {
			t.Errorf("parseCommand(%q) shell wrapped = %v, want %v (got %v)", tt.input, isShellWrapped, tt.wantShell, parts)
		}
	}
}

func TestSession_LogFileContainsOutput(t *testing.T) {
	canFork(t)
	s, err := newSession("log-content", "test", t.TempDir(), "sid-log-content", "echo LOG_MARKER_42", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}

	// Wait for output to be written
	time.Sleep(time.Second)
	s.kill()

	// Read log file
	data, err := os.ReadFile(s.logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	if !strings.Contains(string(data), "LOG_MARKER_42") {
		t.Errorf("log file should contain 'LOG_MARKER_42', got: %q", string(data))
	}
}

// #3389: an agent never inherits Coral's own server settings; what agent tools read stays.
func TestNewSession_AgentEnvHasNoServerSettings(t *testing.T) {
	canFork(t)
	t.Setenv("CORAL_SECRET_KEY", "leak-3389")
	t.Setenv("CORAL_CLOUD_URL", "https://app.coralhub.dev")
	t.Setenv("CORAL_URL", "http://127.0.0.1:9999")
	s, err := newSession("env-3389", "test", t.TempDir(), "sid-env-3389", "env", 80, 24)
	if err != nil {
		t.Fatalf("newSession failed: %v", err)
	}
	defer s.kill()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(string(s.replayBytes()), "TERM=xterm-256color") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	content := string(s.replayBytes())
	if !strings.Contains(content, "TERM=xterm-256color") {
		t.Fatalf("no env output: %q", content)
	}
	for _, gone := range []string{"leak-3389", "CORAL_SECRET_KEY=", "CORAL_CLOUD_URL="} {
		if strings.Contains(content, gone) {
			t.Errorf("the agent saw %s", gone)
		}
	}
	if !strings.Contains(content, "CORAL_URL=http://127.0.0.1:9999") {
		t.Errorf("CORAL_URL (read by coral-board) should reach the agent")
	}
}
