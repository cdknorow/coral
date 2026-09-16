package routes

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
)

// shortTerminalSocketPath returns a path under os.TempDir short enough to fit
// within the Unix domain socket limit (104 bytes on macOS).
func shortTerminalSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "coral-ws-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "t.sock")
}

// newTerminalTestTmuxBackend creates an isolated TmuxBackend for integration tests.
func newTerminalTestTmuxBackend(t *testing.T) (*ptymanager.TmuxBackend, *tmux.Client) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	sock := shortTerminalSocketPath(t)

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

	backend := ptymanager.NewTmuxBackend(client, logDir)

	t.Cleanup(func() {
		exec.Command("tmux", "-S", sock, "kill-server").Run()
	})

	return backend, client
}

// setupTerminalTestServer creates an httptest.Server wired up with a real
// TmuxBackend for WebSocket terminal integration tests.
func setupTerminalTestServer(t *testing.T, backend ptymanager.TerminalBackend) *httptest.Server {
	t.Helper()

	cfg := &config.Config{
		WSPollIntervalS: 1,
		LogDir:          t.TempDir(),
	}

	dbPath := t.TempDir() + "/test.db"
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	handler := NewSessionsHandler(db, cfg, backend, nil, nil)

	r := chi.NewRouter()
	r.Get("/ws/terminal/{name}", handler.WSTerminal)

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	return server
}

// spawnTestSession creates a tmux session and returns the tmux session name.
func spawnTestSession(t *testing.T, backend *ptymanager.TmuxBackend, agentName, sessionID, command string) string {
	t.Helper()
	err := backend.Spawn(agentName, "claude", t.TempDir(), sessionID, command, 80, 24)
	require.NoError(t, err)
	t.Cleanup(func() { backend.Kill(agentName) })
	return naming.SessionName("claude", sessionID)
}

// dialTerminalWS connects a WebSocket client to /ws/terminal/{name}.
func dialTerminalWS(t *testing.T, server *httptest.Server, tmuxName string) (*websocket.Conn, context.Context, context.CancelFunc) {
	t.Helper()
	wsURL := "ws" + server.URL[4:] + "/ws/terminal/" + tmuxName
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.CloseNow() })
	return conn, ctx, cancel
}

// readOneBinaryFrame reads a single binary WebSocket frame.
func readOneBinaryFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) []byte {
	t.Helper()
	msgType, data, err := conn.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageBinary, msgType, "expected binary frame")
	return data
}

// readBinaryUntilContains reads binary frames until accumulated content
// contains the marker, or the context deadline fires.
func readBinaryUntilContains(t *testing.T, ctx context.Context, conn *websocket.Conn, marker string) string {
	t.Helper()
	var buf strings.Builder
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read failed while waiting for %q: %v (accumulated: %q)", marker, err, buf.String())
		}
		if msgType == websocket.MessageBinary {
			buf.Write(data)
			if strings.Contains(buf.String(), marker) {
				return buf.String()
			}
		}
	}
}

// waitForReplayContent polls Replay until the marker appears in the pipe-pane log.
func waitForReplayContent(t *testing.T, backend *ptymanager.TmuxBackend, agentName, marker string) {
	t.Helper()
	for i := 0; i < 30; i++ {
		time.Sleep(200 * time.Millisecond)
		data, _ := backend.Replay(agentName)
		if strings.Contains(string(data), marker) {
			return
		}
	}
	t.Fatalf("timed out waiting for %q in pipe-pane log for %q", marker, agentName)
}

// sendTerminalJSON writes a JSON text frame to the WebSocket.
func sendTerminalJSON(t *testing.T, ctx context.Context, conn *websocket.Conn, msg any) {
	t.Helper()
	err := wsjson.Write(ctx, conn, msg)
	require.NoError(t, err)
}

// TestWSTerminal_ConnectAndReplay verifies that connecting to a session with
// existing output produces a binary replay seed prefixed with clear codes.
func TestWSTerminal_ConnectAndReplay(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)

	tmuxName := spawnTestSession(t, backend, "replay-agent", "aaaaaaaa-1111-2222-3333-444444444444", "echo REPLAY_MARKER_XYZ")
	waitForReplayContent(t, backend, "replay-agent", "REPLAY_MARKER_XYZ")

	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	replay := readOneBinaryFrame(t, ctx, conn)

	clearPrefix := "\x1b[2J\x1b[3J\x1b[H"
	replayStr := string(replay)
	assert.True(t, strings.HasPrefix(replayStr, clearPrefix),
		"replay should start with clear codes, got first 20 bytes: %q", replayStr[:minInt(20, len(replayStr))])
	assert.Contains(t, replayStr, "REPLAY_MARKER_XYZ",
		"replay should contain the session output")

	conn.Close(websocket.StatusNormalClosure, "done")
}

// TestWSTerminal_ResizeUpdatesPane verifies that terminal_resize messages
// actually change the tmux pane dimensions.
func TestWSTerminal_ResizeUpdatesPane(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)

	tmuxName := spawnTestSession(t, backend, "resize-agent", "bbbbbbbb-1111-2222-3333-444444444444", "sleep 120")
	time.Sleep(500 * time.Millisecond)

	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	// Read initial replay
	readOneBinaryFrame(t, ctx, conn)

	// Resize to 120x40
	sendTerminalJSON(t, ctx, conn, map[string]any{
		"type": "terminal_resize",
		"cols": 120,
		"rows": 40,
	})
	time.Sleep(500 * time.Millisecond)

	dimCtx := context.Background()
	size, err := client.DisplayMessage(dimCtx, tmuxName, "#{window_width}x#{window_height}")
	require.NoError(t, err)
	assert.Equal(t, "120x40", strings.TrimSpace(size), "pane should be 120x40 after first resize")

	// Resize to 80x24
	sendTerminalJSON(t, ctx, conn, map[string]any{
		"type": "terminal_resize",
		"cols": 80,
		"rows": 24,
	})
	time.Sleep(500 * time.Millisecond)

	size, err = client.DisplayMessage(dimCtx, tmuxName, "#{window_width}x#{window_height}")
	require.NoError(t, err)
	assert.Equal(t, "80x24", strings.TrimSpace(size), "pane should be 80x24 after second resize")

	conn.Close(websocket.StatusNormalClosure, "done")
}

// TestWSTerminal_LiveStreamAfterReplay verifies that terminal output produced
// after WebSocket connection arrives as live binary frames.
func TestWSTerminal_LiveStreamAfterReplay(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)

	tmuxName := spawnTestSession(t, backend, "live-agent", "cccccccc-1111-2222-3333-444444444444", "sh")
	time.Sleep(500 * time.Millisecond)

	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	// Read replay (shell prompt)
	readOneBinaryFrame(t, ctx, conn)

	// Produce new output via terminal input
	sendTerminalJSON(t, ctx, conn, map[string]any{
		"type": "terminal_input",
		"data": "echo LIVE_STREAM_MARKER_99\n",
	})

	got := readBinaryUntilContains(t, ctx, conn, "LIVE_STREAM_MARKER_99")
	assert.Contains(t, got, "LIVE_STREAM_MARKER_99")
}

// TestWSTerminal_InputNotDuplicated verifies that input sent via WebSocket
// is echoed the expected number of times (no unexpected duplication).
func TestWSTerminal_InputNotDuplicated(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)

	tmuxName := spawnTestSession(t, backend, "nodup-agent", "dddddddd-1111-2222-3333-444444444444", "cat")
	time.Sleep(500 * time.Millisecond)

	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	// Read replay
	readOneBinaryFrame(t, ctx, conn)

	// Send "hello" followed by newline — cat echoes it
	sendTerminalJSON(t, ctx, conn, map[string]any{
		"type": "terminal_input",
		"data": "hello\n",
	})

	// Collect output for a bounded time
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()

	var buf strings.Builder
	for {
		msgType, data, err := conn.Read(readCtx)
		if err != nil {
			break
		}
		if msgType == websocket.MessageBinary {
			buf.Write(data)
		}
	}

	output := buf.String()
	count := strings.Count(output, "hello")
	// Terminal echo + cat output = at most 2 occurrences
	assert.LessOrEqual(t, count, 2,
		"'hello' should appear at most twice (echo + cat output), got %d in: %q", count, output)
	assert.GreaterOrEqual(t, count, 1,
		"'hello' should appear at least once, got %d in: %q", count, output)
}

// TestWSTerminal_SessionRecoveryAfterRestart simulates a server restart by
// creating a NEW TmuxBackend (empty sessions map) against the same tmux
// socket. Connecting via WebSocket should trigger recoverSession and deliver
// the replay seed containing earlier output.
func TestWSTerminal_SessionRecoveryAfterRestart(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)

	sessionID := "eeeeeeee-1111-2222-3333-444444444444"
	tmuxName := spawnTestSession(t, backend, "recover-agent", sessionID, "echo RECOVERY_MARKER_77")
	waitForReplayContent(t, backend, "recover-agent", "RECOVERY_MARKER_77")

	// Create a NEW TmuxBackend with the same tmux socket but empty sessions map
	logDir := filepath.Dir(backend.LogPath("recover-agent"))
	backend2 := ptymanager.NewTmuxBackend(client, logDir)

	server := setupTerminalTestServer(t, backend2)

	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	replay := readOneBinaryFrame(t, ctx, conn)

	assert.Contains(t, string(replay), "RECOVERY_MARKER_77",
		"replay after recovery should contain earlier output")

	conn.Close(websocket.StatusNormalClosure, "done")
}

// TestWSTerminal_BinaryFrameType verifies stream data uses binary frames
// and control messages use JSON text frames.
func TestWSTerminal_BinaryFrameType(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)

	tmuxName := spawnTestSession(t, backend, "frametype-agent", "ffffffff-1111-2222-3333-444444444444", "echo FRAME_TEST")
	waitForReplayContent(t, backend, "frametype-agent", "FRAME_TEST")

	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	// Replay frame must be binary
	msgType, data, err := conn.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageBinary, msgType, "replay should be binary")
	assert.True(t, len(data) > 0, "replay should not be empty")

	// Kill session — may produce terminal_closed JSON text frame
	backend.Kill("frametype-agent")

	closeCtx, closeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer closeCancel()

	for {
		msgType, data, err = conn.Read(closeCtx)
		if err != nil {
			break
		}
		if msgType == websocket.MessageText {
			var msg map[string]string
			if json.Unmarshal(data, &msg) == nil && msg["type"] == "terminal_closed" {
				return
			}
		}
	}
	// Connection close without explicit text frame is acceptable
	t.Log("session ended without explicit terminal_closed frame (acceptable)")
}

// TestWSTerminal_GoroutineCleanup verifies that WebSocket connect/disconnect
// cycles do not leak goroutines.
func TestWSTerminal_GoroutineCleanup(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)

	tmuxName := spawnTestSession(t, backend, "leak-agent", "11111111-2222-3333-4444-555555555555", "sleep 120")
	time.Sleep(500 * time.Millisecond)

	baseline := runtime.NumGoroutine()

	for i := 0; i < 3; i++ {
		func() {
			wsURL := "ws" + server.URL[4:] + "/ws/terminal/" + tmuxName
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			conn, _, err := websocket.Dial(ctx, wsURL, nil)
			require.NoError(t, err)

			readOneBinaryFrame(t, ctx, conn)
			conn.Close(websocket.StatusNormalClosure, "done")
		}()
		time.Sleep(200 * time.Millisecond)
	}

	time.Sleep(1 * time.Second)

	after := runtime.NumGoroutine()
	leaked := after - baseline
	assert.LessOrEqual(t, leaked, 5,
		"goroutine leak: %d new goroutines after 3 connect/disconnect cycles (baseline=%d, after=%d)",
		leaked, baseline, after)
}

// TestWSTerminal_ReplayContainsRecentOutput verifies that the replay seed
// for a session with many lines of output contains the most recent lines
// (not just the beginning). This validates the server-side behavior that
// supports the client-side scrollToBottom on session switch.
func TestWSTerminal_ReplayContainsRecentOutput(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)

	// Produce 500 lines of output so the replay is non-trivial
	tmuxName := spawnTestSession(t, backend, "scroll-agent", "22222222-3333-4444-5555-666666666666",
		"seq 1 500")
	waitForReplayContent(t, backend, "scroll-agent", "500")

	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	replay := readOneBinaryFrame(t, ctx, conn)
	replayStr := string(replay)

	// The tail of the replay should contain recent lines (near 500)
	assert.Contains(t, replayStr, "500", "replay should contain the last line of output")
	assert.Contains(t, replayStr, "499", "replay should contain near-last lines")
	assert.Contains(t, replayStr, "498", "replay should contain near-last lines")

	conn.Close(websocket.StatusNormalClosure, "done")
}

// TestWSTerminal_ResizeOnConnect verifies that a client sending terminal_resize
// immediately after connecting (before any input) correctly updates the tmux
// pane dimensions. This mirrors the client-side onopen resize behavior.
func TestWSTerminal_ResizeOnConnect(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)

	tmuxName := spawnTestSession(t, backend, "roc-agent", "33333333-4444-5555-6666-777777777777", "sleep 120")
	time.Sleep(500 * time.Millisecond)

	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	// Read replay
	readOneBinaryFrame(t, ctx, conn)

	// Immediately send resize (simulating client onopen behavior)
	sendTerminalJSON(t, ctx, conn, map[string]any{
		"type": "terminal_resize",
		"cols": 132,
		"rows": 43,
	})
	time.Sleep(500 * time.Millisecond)

	// Verify the resize was applied
	dimCtx := context.Background()
	size, err := client.DisplayMessage(dimCtx, tmuxName, "#{window_width}x#{window_height}")
	require.NoError(t, err)
	assert.Equal(t, "132x43", strings.TrimSpace(size),
		"tmux pane should match resize-on-connect dimensions")

	// Verify a second client also sees the updated dimensions
	conn2, ctx2, cancel2 := dialTerminalWS(t, server, tmuxName)
	defer cancel2()
	readOneBinaryFrame(t, ctx2, conn2)

	// Send a different resize from the second client
	sendTerminalJSON(t, ctx2, conn2, map[string]any{
		"type": "terminal_resize",
		"cols": 100,
		"rows": 30,
	})
	time.Sleep(500 * time.Millisecond)

	size, err = client.DisplayMessage(dimCtx, tmuxName, "#{window_width}x#{window_height}")
	require.NoError(t, err)
	assert.Equal(t, "100x30", strings.TrimSpace(size),
		"second client resize-on-connect should also update pane")

	conn.Close(websocket.StatusNormalClosure, "done")
	conn2.Close(websocket.StatusNormalClosure, "done")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
