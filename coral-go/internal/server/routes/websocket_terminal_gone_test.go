package routes

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

// readWSTypes reads frames until a JSON frame of type stop arrives, the socket
// closes, or the deadline passes, and returns the JSON frame types seen in order.
func readWSTypes(t *testing.T, ctx context.Context, conn *websocket.Conn, stop string, within time.Duration) []string {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	var types []string
	for {
		typ, data, err := conn.Read(rctx)
		if err != nil {
			return types
		}
		if typ != websocket.MessageText {
			continue
		}
		var m struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			NotSent bool   `json:"not_sent"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		types = append(types, m.Type)
		if m.Type == "terminal_gone" {
			assert.Equal(t, "terminal_gone", m.Code)
		}
		if m.Type == stop {
			return types
		}
	}
}

func killTmuxSession(t *testing.T, socket, name string) {
	t.Helper()
	require.NoError(t, exec.Command("tmux", "-S", socket, "kill-session", "-t", "="+name).Run())
}

// #3218, QA's live finding: with the terminal socket open, input to an agent whose
// pane was killed was dropped silently. The client is now told (terminal_gone,
// like the HTTP 409), and the stream ends (terminal_closed).
func TestWSTerminal_InputToAGonePaneIsReportedNotDropped(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "gone-agent", "a1b2c3d4-1111-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	killTmuxSession(t, client.SocketPath, tmuxName)
	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_input", "data": "echo lost\r"})

	types := readWSTypes(t, ctx, conn, "terminal_closed", 8*time.Second)
	require.Contains(t, types, "terminal_gone", "the input was refused out loud (got %v)", types)
	assert.Contains(t, types, "terminal_closed", "and the stream ended (got %v)", types)
}

// The stream ends on its own once the tmux session is gone, so the dashboard learns
// the terminal closed even before it sends anything.
func TestWSTerminal_StreamEndsWhenTheSessionIsGone(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "ended-agent", "b2c3d4e5-1111-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	killTmuxSession(t, client.SocketPath, tmuxName)
	types := readWSTypes(t, ctx, conn, "terminal_closed", 8*time.Second)
	assert.Equal(t, []string{"terminal_closed"}, types, "terminal_closed within a few seconds, nothing else")
}

// The session check never ends a live stream: input still reaches the pane and its
// output still streams after several checks.
func TestWSTerminal_LiveStreamOutlastsTheSessionCheck(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "live-agent", "c3d4e5f6-1111-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	time.Sleep(3500 * time.Millisecond) // sleep-ok: proves a negative, three session checks pass while the session lives
	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_input", "data": "echo STILL_LIVE_4242\r"})
	out := readBinaryUntilContains(t, ctx, conn, "STILL_LIVE_4242")
	assert.Contains(t, out, "STILL_LIVE_4242")
}
