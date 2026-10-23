package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/internal/terminalinput"
)

// readWSFrame returns the next JSON text frame that satisfies match (binary output and
// other frames are skipped), or nil when the deadline passes or the socket closes.
func readWSFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, within time.Duration, match func(map[string]any) bool) map[string]any {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	for {
		typ, data, err := conn.Read(rctx)
		if err != nil {
			return nil
		}
		if typ != websocket.MessageText {
			continue
		}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil && match(m) {
			return m
		}
	}
}

func frameType(want string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["type"] == want }
}

// #3276: the server says it answers input (terminal_hello), then acknowledges each
// terminal_input that carries an id once it is delivered, echoing the id.
func TestWSTerminal_InputWithAnIDIsAcknowledged(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "ack-agent", "d4e5f6a7-1111-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	hello := readWSFrame(t, ctx, conn, 5*time.Second, func(m map[string]any) bool { return true })
	require.NotNil(t, hello, "a first text frame")
	assert.Equal(t, "terminal_hello", hello["type"], "the first text frame announces acks: %v", hello)
	assert.Equal(t, true, hello["input_acks"])

	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_input", "data": "echo ACKED_$((40+2))", "id": 7})
	ack := readWSFrame(t, ctx, conn, 5*time.Second, frameType("terminal_input_ack"))
	require.NotNil(t, ack, "an ack for the input")
	assert.EqualValues(t, 7, ack["id"], "the ack carries the input's id")

	// The input really reached the pane.
	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_input", "data": "\r", "id": 8})
	out := readBinaryUntilContains(t, ctx, conn, "ACKED_42")
	assert.Contains(t, out, "ACKED_42")
}

// Input without an id (an older client) gets no answer, so nothing changes for it.
func TestWSTerminal_InputWithoutAnIDGetsNoAck(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "noid-agent", "e5f6a7b8-1111-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_input", "data": "echo no-id"})
	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_resize", "cols": 100, "rows": 40})
	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_input", "data": "\r", "id": 21})
	// Frames are answered in order: the first ack seen is the id'd one, so the others got none.
	ack := readWSFrame(t, ctx, conn, 5*time.Second, frameType("terminal_input_ack"))
	require.NotNil(t, ack)
	assert.EqualValues(t, 21, ack["id"], "the input without an id and the resize were not acknowledged")
}

// Input with an id to a gone pane: terminal_gone carries that id, and no ack comes.
func TestWSTerminal_GoneCarriesTheInputID(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "goneid-agent", "f6a7b8c9-1111-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()
	waitTerminalStreamLive(t, ctx, conn)

	killTmuxSession(t, client.SocketPath, tmuxName)
	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_input", "data": "echo lost", "id": 33})
	var acked bool
	gone := readWSFrame(t, ctx, conn, 8*time.Second, func(m map[string]any) bool {
		if m["type"] == "terminal_input_ack" && m["id"] == float64(33) {
			acked = true
		}
		return m["type"] == "terminal_gone"
	})
	require.NotNil(t, gone, "terminal_gone for the input")
	assert.EqualValues(t, 33, gone["id"], "it names the refused input")
	assert.Equal(t, true, gone["not_sent"])
	assert.False(t, acked, "a refused input is never acknowledged")
}

// A terminal that is already gone when the socket opens: terminal_closed and a normal
// close ('session ended'), not an InternalError 'attach failed' (#3276).
func TestWSTerminal_GoneAtAttachSaysTerminalClosed(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "goneattach-agent", "a7b8c9d0-1111-4222-8333-444455556666", "")
	killTmuxSession(t, client.SocketPath, tmuxName)
	_ = backend.Kill(tmuxName) // forget it too, as after a crash and a server restart
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	types := readWSTypes(t, ctx, conn, "terminal_closed", 5*time.Second)
	assert.Equal(t, []string{"terminal_closed"}, types)
	_, _, err := conn.Read(ctx)
	var ce websocket.CloseError
	require.True(t, errors.As(err, &ce), "the socket closes: %v", err)
	assert.Equal(t, websocket.StatusNormalClosure, ce.Code)
	assert.Equal(t, "session ended", ce.Reason)
}

// A frame whose id is not an integer (a third-party client) is still typed, as before ids
// existed, and gets no answer (QA's #3276 note).
func TestWSTerminal_NonIntegerIDIsTypedWithoutAnAck(t *testing.T) {
	backend, _ := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServer(t, backend)
	tmuxName := spawnTestSession(t, backend, "strid-agent", "b8c9d0e1-1111-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_input", "data": "echo STRID_$((3*3))\r", "id": "abc"})
	out := readBinaryUntilContains(t, ctx, conn, "STRID_9")
	assert.Contains(t, out, "STRID_9", "the input was typed")
	// {"id": null} is no id either (it used to decode as id 0 and get an ack).
	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_input", "data": "echo NULLID_$((2*4))\r", "id": nil})
	out = readBinaryUntilContains(t, ctx, conn, "NULLID_8")
	assert.Contains(t, out, "NULLID_8", "the input was typed")
	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "terminal_input", "data": "", "id": 5})
	ack := readWSFrame(t, ctx, conn, 5*time.Second, frameType("terminal_input_ack"))
	require.NotNil(t, ack)
	assert.EqualValues(t, 5, ack["id"], "the string and null ids got no ack; the next integer id did")
}

// #4587: a message from Chat's composer is one composer_send frame. The server types it and submits it (its Enter
// too), and answers with the frame's id once it was submitted: here the shell ran it.
func TestWSTerminal_ComposerSendIsSubmittedThenAcknowledged(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	server := setupTerminalTestServerWith(t, backend, ptymanager.NewTmuxSessionTerminal(client))
	tmuxName := spawnTestSession(t, backend, "composer-agent", "f6a7b8c9-1111-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	sendTerminalJSON(t, ctx, conn, map[string]any{"type": "composer_send", "data": "echo COMPOSED_$((40+2))", "id": 31})
	// The shell's output and the ack arrive in either order: read both.
	var out strings.Builder
	var ack map[string]any
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	for ack == nil || !strings.Contains(out.String(), "COMPOSED_42") {
		typ, data, err := conn.Read(rctx)
		require.NoError(t, err, "waiting for the ack and the output (so far %q, ack %v)", out.String(), ack)
		if typ == websocket.MessageBinary {
			out.Write(data)
			continue
		}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil && m["type"] == "terminal_input_ack" {
			ack = m
		}
	}
	assert.EqualValues(t, 31, ack["id"])
	assert.Contains(t, out.String(), "COMPOSED_42", "typed and submitted: the shell ran it, with no separate Enter")
}

// setupTerminalTestServerWith is setupTerminalTestServer with the session terminal production has (a composer
// message goes through it).
func setupTerminalTestServerWith(t *testing.T, backend ptymanager.TerminalBackend, terminal ptymanager.SessionTerminal) *httptest.Server {
	t.Helper()
	db, err := storetest.Open(t, t.TempDir()+"/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	handler := NewSessionsHandler(db, &config.Config{WSPollIntervalS: 1, LogDir: t.TempDir()}, backend, terminal, nil)
	r := chi.NewRouter()
	r.Get("/ws/terminal/{name}", handler.WSTerminal)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	return server
}

// blockingTerminal holds every SendInput until release is closed, counting them (#4587's flood test).
type blockingTerminal struct {
	ptymanager.SessionTerminal
	release chan struct{}
	entered atomic.Int32
}

func (b *blockingTerminal) SendInput(ctx context.Context, _ terminalinput.Origin, name, text, agentType, sessionID string) error {
	b.entered.Add(1)
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.SessionTerminal.SendInput(ctx, terminalinput.Unclassified, name, text, agentType, sessionID)
}

// A flood of composer_send frames on one socket (Security's MUST on #4587): at most composerSendsPerSocket deliveries
// are under way; the rest are refused at once, as busy and not sent, and nothing more is typed for them.
func TestWSTerminal_ComposerSendFloodIsBounded(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	term := &blockingTerminal{SessionTerminal: ptymanager.NewTmuxSessionTerminal(client), release: make(chan struct{})}
	server := setupTerminalTestServerWith(t, backend, term)
	tmuxName := spawnTestSession(t, backend, "composer-flood", "f6a7b8c9-2222-4222-8333-444455556666", "")
	conn, ctx, cancel := dialTerminalWS(t, server, tmuxName)
	defer cancel()

	const frames = 100
	for i := 1; i <= frames; i++ {
		sendTerminalJSON(t, ctx, conn, map[string]any{"type": "composer_send", "data": fmt.Sprintf("echo flood_%d", i), "id": i})
	}
	busy := map[float64]bool{}
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	for len(busy) < frames-composerSendsPerSocket {
		typ, data, err := conn.Read(rctx)
		require.NoError(t, err, "waiting for the busy answers (%d so far)", len(busy))
		if typ == websocket.MessageBinary {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(data, &m))
		require.NotEqual(t, "terminal_input_ack", m["type"], "nothing is acknowledged while every delivery is held")
		if m["type"] == "terminal_input_failed" {
			assert.Equal(t, "agent_busy", m["code"])
			assert.Equal(t, true, m["not_sent"])
			busy[m["id"].(float64)] = true
		}
	}
	// The delivery that holds the agent's turn reads the agent's signals first (a message is held behind a box that asks the person, #6610), so it may not have reached the terminal yet.
	require.Eventually(t, func() bool { return term.entered.Load() == 1 }, 5*time.Second, 5*time.Millisecond, "one delivery typing")
	assert.EqualValues(t, 1, term.entered.Load(), "one delivery typing; the others under way wait their turn for this agent")
	close(term.release)
	acks := 0
	for acks < composerSendsPerSocket {
		typ, data, err := conn.Read(rctx)
		require.NoError(t, err, "waiting for the acks (%d so far)", acks)
		if typ == websocket.MessageBinary {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(data, &m))
		if m["type"] == "terminal_input_ack" {
			assert.False(t, busy[m["id"].(float64)], "an acknowledged send was not one refused")
			acks++
		}
	}
	assert.EqualValues(t, composerSendsPerSocket, term.entered.Load(), "only the deliveries under way were typed")
}
