package routes

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/ptymanager"
)

// lagBackend is a terminal backend for the socket's own behaviour (#4161): the test feeds and
// closes the subscriber's channel and says whether a close was a lag cut-off. Methods the
// terminal socket does not call are left to the embedded nil interface.
type lagBackend struct {
	ptymanager.TerminalBackend
	mu       sync.Mutex
	ch       chan []byte
	attaches int
	lagged   bool
	unsubbed chan struct{}
}

func newLagBackend() *lagBackend { return &lagBackend{unsubbed: make(chan struct{})} }

func (b *lagBackend) Attach(name, id string) (<-chan []byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attaches++
	b.ch = make(chan []byte, 256)
	return b.ch, nil
}

func (b *lagBackend) Unsubscribe(name, id string) { close(b.unsubbed) }

func (b *lagBackend) Replay(name string) ([]byte, error) { return []byte("REPLAY"), nil }

func (b *lagBackend) Lagged(name, id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	l := b.lagged
	b.lagged = false
	return l
}

// cutOff closes the current channel as the backend does for a viewer that fell behind.
func (b *lagBackend) cutOff(lag bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lagged = lag
	close(b.ch)
}

func (b *lagBackend) send(data []byte) bool {
	b.mu.Lock()
	ch := b.ch
	b.mu.Unlock()
	select {
	case ch <- data:
		return true
	default:
		return false
	}
}

func (b *lagBackend) attachCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attaches
}

// readReplay reads frames until the replay seed (clear codes, then the replay).
func readReplay(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	for {
		typ, data, err := conn.Read(ctx)
		require.NoError(t, err)
		if typ == websocket.MessageBinary && bytes.HasPrefix(data, []byte("\x1b[2J\x1b[3J\x1b[H")) && bytes.HasSuffix(data, []byte("REPLAY")) {
			return
		}
	}
}

// A viewer cut off for lagging is sent a fresh replay on the same socket; one that keeps
// lagging is closed with "try again later"; a terminal that really ends still says so.
func TestWSTerminal_LaggingViewerIsResyncedThenClosed(t *testing.T) {
	b := newLagBackend()
	server := setupTerminalTestServer(t, b)
	conn, ctx, cancel := dialTerminalWS(t, server, lagAgent)
	defer cancel()
	readReplay(t, ctx, conn)

	for i := 1; i <= resyncLimit; i++ {
		b.cutOff(true)
		readReplay(t, ctx, conn)
		assert.Equal(t, i+1, b.attachCount(), "re-attached after cut-off %d", i)
		require.True(t, b.send([]byte("live")))
	}
	b.cutOff(true)
	var err error
	for err == nil {
		_, _, err = conn.Read(ctx)
	}
	assert.Equal(t, websocket.StatusTryAgainLater, websocket.CloseStatus(err), "lagging again within the minute: closed, so the dashboard pauses: %v", err)

	b2 := newLagBackend()
	server2 := setupTerminalTestServer(t, b2)
	conn2, ctx2, cancel2 := dialTerminalWS(t, server2, lagAgent)
	defer cancel2()
	readReplay(t, ctx2, conn2)
	b2.cutOff(false)
	for {
		typ, data, err := conn2.Read(ctx2)
		require.NoError(t, err, "terminal_closed comes before the close")
		if typ == websocket.MessageText && bytes.Contains(data, []byte("terminal_closed")) {
			break
		}
	}
	assert.Equal(t, 1, b2.attachCount(), "an ended terminal is not attached again")
}

// A client that stops reading is dropped once a write has waited wsWriteTimeout, and its
// subscription is released, while the agent's output keeps flowing (#4161).
func TestWSTerminal_StuckClientIsDropped(t *testing.T) {
	prev := wsWriteTimeout
	wsWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { wsWriteTimeout = prev })

	b := newLagBackend()
	server := setupTerminalTestServer(t, b)
	conn, ctx, cancel := dialTerminalWS(t, server, lagAgent)
	defer cancel()
	readReplay(t, ctx, conn)
	var hello map[string]any
	require.NoError(t, wsjson.Read(ctx, conn, &hello))
	// From here the client reads nothing; the agent prints 32 KiB frames.
	frame := bytes.Repeat([]byte("x"), 32<<10)
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-b.unsubbed:
			return
		case <-deadline:
			t.Fatal("a client that stopped reading was never dropped")
		default:
		}
		b.send(frame)
		time.Sleep(time.Millisecond)
	}
}

func TestResyncBudget(t *testing.T) {
	var b resyncBudget
	now := time.Now()
	for i := 0; i < resyncLimit; i++ {
		assert.True(t, b.allow(now.Add(time.Duration(i)*time.Second)))
	}
	assert.False(t, b.allow(now.Add(10*time.Second)), "a fourth within the minute")
	assert.True(t, b.allow(now.Add(resyncWindow+time.Second)), "the first has aged out")
}

// lagAgent is a terminal name in the form the socket accepts without a session_id.
const lagAgent = "claude-11111111-2222-3333-4444-555555555555"
