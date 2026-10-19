package background

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5099 review (QA Engineer's test, adapted): the hub keeps at most 5000 sessions per remote machine, but a session's size is bounded only by the 32 MiB a message
// may hold, so a machine that is not safe to trust could fill the hub's memory (5000 sessions of a few MiB each is tens of gigabytes). What the hub holds for ONE
// machine is bounded in bytes too: a row over maxRemoteRowBytes is not kept, and the rows kept never pass maxRemoteBytes. A hostile remote sends rows through its feed;
// the hub's snapshot of that machine must stay small, and a small row sent last must still get in (the hub is not stuck).
func feedBytesRig(t *testing.T) (*RemoteAgentHub, *websocket.Conn) {
	t.Helper()
	a := newFakeRemote(t, sess("one", "a-1"))
	reg := newFakeRegistry()
	reg.put("a", a.URL)
	h := fastHub(reg)
	startHub(t, h)
	var conn *websocket.Conn
	select {
	case conn = <-a.feedConn:
	case <-time.After(5 * time.Second):
		t.Fatal("the hub never opened the remote's feed")
	}
	conn.SetReadLimit(1 << 20)
	return h, conn
}

func sendRow(t *testing.T, conn *websocket.Conn, name string, promptBytes int) {
	t.Helper()
	row := map[string]any{"name": name, "session_id": "id-" + name, "status": "Working", "prompt": strings.Repeat("x", promptBytes)}
	require.NoError(t, wsjson.Write(context.Background(), conn, map[string]any{"type": "coral_diff", "changed": []map[string]any{row}}))
}

func heldBytes(t *testing.T, h *RemoteAgentHub) (rows, bytes int) {
	t.Helper()
	b, err := json.Marshal(h.Snapshot().Sessions)
	require.NoError(t, err)
	return len(h.Snapshot().Sessions), len(b)
}

func hubHas(h *RemoteAgentHub, name string) bool {
	for _, s := range h.Snapshot().Sessions {
		if s["name"] == name {
			return true
		}
	}
	return false
}

// 24 rows of 4 MiB (96 MiB, QA's case): every one is over the row limit and none is kept; a small row sent after them is.
func TestRemoteFeedRetainsABoundedNumberOfBytes(t *testing.T) {
	h, conn := feedBytesRig(t)
	for i := 0; i < 24; i++ {
		sendRow(t, conn, fmt.Sprintf("hostile-%d", i), 4<<20)
	}
	sendRow(t, conn, "after", 100)
	require.Eventually(t, func() bool { return hubHas(h, "after") }, 20*time.Second, 50*time.Millisecond, "the hub took the small row sent after the big ones")
	rows, bytes := heldBytes(t, h)
	assert.Less(t, bytes, maxRemoteBytes, "the hub holds %d MiB of one machine's sessions", bytes>>20)
	for i := 0; i < 24; i++ {
		assert.False(t, hubHas(h, fmt.Sprintf("hostile-%d", i)), "a 4 MiB row is over the row limit (%d rows held)", rows)
	}
}

// Rows each under the row limit still stop at the machine's byte budget: 200 rows of 200 KiB are 40 MiB sent against 32.
func TestRemoteFeedStopsAtTheByteBudget(t *testing.T) {
	h, conn := feedBytesRig(t)
	const rows, rowBytes = 200, 200 << 10
	for i := 0; i < rows; i++ {
		sendRow(t, conn, fmt.Sprintf("big-%d", i), rowBytes)
	}
	sendRow(t, conn, "after", 100)
	require.Eventually(t, func() bool { return hubHas(h, "big-0") && hubHas(h, "big-100") }, 20*time.Second, 50*time.Millisecond)
	time.Sleep(500 * time.Millisecond) // sleep-ok: the rest of the 200 rows arrive and are refused; there is no event for a refusal
	n, bytes := heldBytes(t, h)
	assert.LessOrEqual(t, bytes, maxRemoteBytes+n*512, "held %d MiB in %d rows against a %d MiB budget", bytes>>20, n, maxRemoteBytes>>20)
	assert.Less(t, n, rows, "the budget stopped some of the %d rows", rows)
	assert.True(t, hubHas(h, "big-0"), "the first rows are kept")
	assert.False(t, hubHas(h, fmt.Sprintf("big-%d", rows-1)), "a row past the budget is not")
}
