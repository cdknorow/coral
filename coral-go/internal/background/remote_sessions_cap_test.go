package background

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rowJSON(id string, extra map[string]any) json.RawMessage {
	m := map[string]any{"session_id": id, "name": "n-" + id}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// The hub keeps at most maxRemoteSessions sessions for one remote machine, from its list or its feed (#5099).
func TestRemoteSessionListIsCapped(t *testing.T) {
	raws := make([]json.RawMessage, 0, maxRemoteSessions+50)
	for i := 0; i < maxRemoteSessions+50; i++ {
		raws = append(raws, rowJSON(fmt.Sprintf("s%d", i), nil))
	}
	body, err := json.Marshal(raws)
	require.NoError(t, err)
	list, err := parseLiveList(body)
	require.NoError(t, err)
	assert.Len(t, list, maxRemoteSessions)
	assert.Equal(t, "s0", list[0]["session_id"], "the first ones are kept, in order")
}

// A row of another server is dropped BEFORE the cap counts it: more foreign rows than the cap, then the machine's own, and every own row is kept (list and feed).
func TestForeignRowsAreDroppedBeforeTheCap(t *testing.T) {
	var raws []json.RawMessage
	for i := 0; i < maxRemoteSessions+500; i++ {
		raws = append(raws, rowJSON(fmt.Sprintf("f%d", i), map[string]any{"server": "other"}))
	}
	for i := 0; i < 10; i++ {
		raws = append(raws, rowJSON(fmt.Sprintf("own%d", i), nil))
	}
	body, _ := json.Marshal(raws)
	list, err := parseLiveList(body)
	require.NoError(t, err)
	require.Len(t, list, 10, "the machine's own rows, none lost to the foreign ones")
	assert.Equal(t, "own0", list[0]["session_id"])

	rows := newRemoteRows()
	for _, raw := range raws {
		rows.add(raw)
	}
	assert.Len(t, rows.sessions(), 10)
}

// One row over maxRemoteRowBytes is not kept; a list holding such rows and small ones keeps the small ones; the byte budget skips what does not fit and takes a
// later smaller row that does.
func TestRemoteListRowAndByteLimits(t *testing.T) {
	big := strings.Repeat("x", maxRemoteRowBytes) // the row is a little over the limit with its other fields
	var raws []json.RawMessage
	raws = append(raws, rowJSON("huge", map[string]any{"prompt": big}), rowJSON("ok1", nil))
	body, _ := json.Marshal(raws)
	list, err := parseLiveList(body)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "ok1", list[0]["session_id"])

	// 200 KiB rows against the 32 MiB budget: the budget stops them, a later small row still gets in.
	raws = raws[:0]
	for i := 0; i < 200; i++ {
		raws = append(raws, rowJSON(fmt.Sprintf("b%d", i), map[string]any{"prompt": strings.Repeat("y", 200<<10)}))
	}
	raws = append(raws, rowJSON("small", nil))
	body, _ = json.Marshal(raws)
	list, err = parseLiveList(body)
	require.NoError(t, err)
	total := 0
	for _, m := range list {
		b, _ := json.Marshal(m)
		total += len(b)
	}
	assert.LessOrEqual(t, total, maxRemoteBytes+len(list)*512)
	assert.Less(t, len(list), 201)
	assert.Equal(t, "small", list[len(list)-1]["session_id"], "a small row after the budget is full still fits")
}

func TestRemoteFeedStopsGrowingAtTheCap(t *testing.T) {
	rows := newRemoteRows()
	for i := 0; i < maxRemoteSessions+100; i++ {
		kept := rows.add(rowJSON(fmt.Sprintf("s%d", i), map[string]any{"i": i}))
		assert.Equal(t, i < maxRemoteSessions, kept, "session %d", i)
	}
	assert.Len(t, rows.sessions(), maxRemoteSessions)
	// A session already known still updates at the cap; a removed one frees a place.
	assert.True(t, rows.add(rowJSON("s0", map[string]any{"i": -1})))
	assert.EqualValues(t, -1, rows.sessions()[0]["i"])
	assert.False(t, rows.add(rowJSON("new", nil)))
	rows.remove("s1")
	assert.True(t, rows.add(rowJSON("new", nil)))
	assert.Len(t, rows.sessions(), maxRemoteSessions)
}

// An update that makes a known row oversize takes the old row out (the machine says it is now something the hub will not hold); an update that would pass the
// budget leaves the old row in place. The byte count and the signature follow every change.
func TestRemoteFeedRowReplacementKeepsBytesAndSignatureRight(t *testing.T) {
	rows := newRemoteRows()
	require.True(t, rows.add(rowJSON("a", nil)))
	require.True(t, rows.add(rowJSON("b", nil)))
	sig0, bytes0 := rows.signature(), rows.bytes
	require.True(t, rows.add(rowJSON("a", map[string]any{"status": "Idle"})))
	assert.NotEqual(t, sig0, rows.signature(), "a changed row changes the signature")
	assert.Greater(t, rows.bytes, bytes0)

	rows.add(rowJSON("a", map[string]any{"prompt": strings.Repeat("z", maxRemoteRowBytes)}))
	_, still := rows.rows["a"]
	assert.False(t, still, "an oversize update removes the old row")
	rows.remove("b")
	assert.Zero(t, rows.bytes, "bytes follow removals")
	assert.Zero(t, rows.sum)
	rows.add(rowJSON("a", nil))
	rows.add(rowJSON("b", nil))
	again := rows.signature()
	rows.remove("a")
	rows.add(rowJSON("a", nil))
	assert.Equal(t, again, rows.signature(), "the same rows give the same signature, whatever the order of arrival")
}
