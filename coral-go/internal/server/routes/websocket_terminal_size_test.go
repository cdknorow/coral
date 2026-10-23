package routes

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tmuxPaneSize reads the pane's size straight from tmux on the test socket.
func tmuxPaneSize(t *testing.T, socket, name string) (int, int) {
	t.Helper()
	out, err := exec.Command("tmux", "-S", socket, "display-message", "-p", "-t", "="+name+":", "#{pane_width} #{pane_height}").Output()
	require.NoError(t, err)
	var cols, rows int
	_, err = fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &cols, &rows)
	require.NoError(t, err)
	return cols, rows
}

func sizeFrame(cols, rows int) func(map[string]any) bool {
	return func(m map[string]any) bool {
		return m["type"] == "terminal_size" && m["cols"] == float64(cols) && m["rows"] == float64(rows)
	}
}

// #3422: the socket reports the pane's real size after terminal_hello, answers a resize with the
// size the pane now has, and reports a resize made elsewhere (another client, tmux itself), so a
// viewer that does not set the size can render at the pane's.
func TestWSTerminal_ReportsThePaneSize(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "size-agent", "b8c9d0e1-1111-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	var order []string
	first := readWSFrame(t, ctx, conn, 5*time.Second, func(m map[string]any) bool {
		order = append(order, fmt.Sprint(m["type"]))
		return m["type"] == "terminal_size"
	})
	require.NotNil(t, first, "a terminal_size frame on open")
	assert.Equal(t, []string{"terminal_hello", "terminal_size"}, order, "hello first (clients expect it), then the size")
	cols, rows := tmuxPaneSize(t, client.SocketPath, tmuxName)
	assert.EqualValues(t, cols, first["cols"], "the pane's real width")
	assert.EqualValues(t, rows, first["rows"], "the pane's real height")
	assert.Equal(t, true, first["initial"], "the first report is marked initial (it never answers a resize)")
	assert.Nil(t, first["seq"])

	// A resize on this socket is answered with the pane's new size and the resize's seq.
	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_resize", "cols": 97, "rows": 31, "seq": 5})
	answer := readWSFrame(t, ctx, conn, 5*time.Second, sizeFrame(97, 31))
	require.NotNil(t, answer, "the answer to this socket's resize")
	assert.EqualValues(t, 5, answer["seq"], "the answer echoes the resize's seq")
	assert.Nil(t, answer["initial"])
	// Asked again at the same size: still answered (the client waits for its latest seq).
	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_resize", "cols": 97, "rows": 31, "seq": 6})
	again := readWSFrame(t, ctx, conn, 5*time.Second, sizeFrame(97, 31))
	require.NotNil(t, again, "an unchanged size is answered too")
	assert.EqualValues(t, 6, again["seq"])
	cols, rows = tmuxPaneSize(t, client.SocketPath, tmuxName)
	assert.Equal(t, [2]int{97, 31}, [2]int{cols, rows}, "tmux has the size")

	// A resize made elsewhere is reported too (checked every terminalSizeEvery).
	require.NoError(t, exec.Command("tmux", "-S", client.SocketPath, "resize-window", "-t", "="+tmuxName+":", "-x", "143", "-y", "37").Run())
	other := readWSFrame(t, ctx, conn, terminalSizeEvery+4*time.Second, sizeFrame(143, 37))
	require.NotNil(t, other, "a resize by another client reaches this viewer")
	assert.Nil(t, other["seq"], "a change made elsewhere answers nothing")

	// A second viewer is told the current size when it opens.
	conn2, ctx2, cancel2 := dialTerminalWS(t, server, tmuxName)
	defer cancel2()
	require.NotNil(t, readWSFrame(t, ctx2, conn2, 5*time.Second, sizeFrame(143, 37)), "a new viewer gets the pane's size")

	// The second viewer resizes: the first is told.
	sendTerminalJSON(t, ctx2, conn2, map[string]any{"type": "terminal_resize", "cols": 60, "rows": 20, "seq": 1})
	theirs := readWSFrame(t, ctx, conn, terminalSizeEvery+4*time.Second, sizeFrame(60, 20))
	require.NotNil(t, theirs, "the other viewer's resize reaches the first")
	assert.Nil(t, theirs["seq"], "without the other socket's seq")
	conn2.Close(websocket.StatusNormalClosure, "")
}

// No size frame repeats while nothing changes.
func TestWSTerminal_SizeIsNotRepeated(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "size-quiet", "c9d0e1f2-1111-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	require.NotNil(t, readWSFrame(t, ctx, conn, 5*time.Second, frameType("terminal_size")))
	again := readWSFrame(t, ctx, conn, 2*terminalSizeEvery+500*time.Millisecond, frameType("terminal_size"))
	assert.Nil(t, again, "no second terminal_size without a change")

}

// #6706: a viewer that opens right after a resize made elsewhere is told the size the pane has now, not the one the shared pane list had a moment ago.
func TestWSTerminal_FirstSizeIsFreshAfterAResizeElsewhere(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "size-fresh", "d0e1f2a3-1111-4222-8333-444455556666", "")
	_, _, err := backend.PaneSize(tmuxName) // the shared pane list now has the old size
	require.NoError(t, err)
	require.NoError(t, exec.Command("tmux", "-S", client.SocketPath, "resize-window", "-t", "="+tmuxName+":", "-x", "91", "-y", "29").Run())

	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()
	first := readWSFrame(t, ctx, conn, 5*time.Second, frameType("terminal_size"))
	require.NotNil(t, first)
	assert.Equal(t, true, first["initial"])
	assert.EqualValues(t, 91, first["cols"], "the size now, not the list's")
	assert.EqualValues(t, 29, first["rows"])
}
