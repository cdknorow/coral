package background

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5099 round 2 review: whatever sequence of adds (new, replacing, oversize, past the budget), removals and resets a machine sends, the running totals of
// remoteRows equal what is held, the held rows never pass the byte budget or the count, and the signature follows the content.
// Mutation: forget the old row's size or hash when a row is replaced or removed (the totals drift and the budget closes on rows that are gone).
func TestRemoteRowsTotalsNeverDriftFromWhatIsHeld(t *testing.T) {
	rng := rand.New(rand.NewSource(5099))
	rows := newRemoteRows()
	row := func(id string, pad int) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"session_id":%q,"name":"n","pad":%q}`, id, strings.Repeat("x", pad)))
	}
	check := func(step int) {
		bytes, sum, n := 0, uint64(0), 0
		for k, r := range rows.rows {
			bytes += r.size
			sum += r.sum
			n++
			assert.Equal(t, k, FeedKey(r.m), "step %d: the row is held under its own key", step)
		}
		require.Equal(t, bytes, rows.bytes, "step %d: bytes", step)
		require.Equal(t, sum, rows.sum, "step %d: signature sum", step)
		require.LessOrEqual(t, bytes, maxRemoteBytes, "step %d: the budget", step)
		require.LessOrEqual(t, n, maxRemoteSessions, "step %d: the count", step)
		require.Len(t, rows.sessions(), n, "step %d: the list has every held row once", step)
	}
	for step := 0; step < 4000; step++ {
		switch rng.Intn(10) {
		case 0:
			rows.remove(fmt.Sprintf("s%d", rng.Intn(60)))
		case 1:
			if rng.Intn(25) == 0 {
				rows.reset()
			}
		default:
			pad := []int{10, 1000, 100 << 10, 200 << 10, maxRemoteRowBytes - 100, maxRemoteRowBytes + 10}[rng.Intn(6)]
			rows.add(row(fmt.Sprintf("s%d", rng.Intn(60)), pad)) // 60 ids: many replacements; 200 KiB pads: the 32 MiB budget is reached
		}
		check(step)
	}
	// the signature follows the content: replace with other content, then put it back
	rows.reset()
	rows.add(row("a", 10))
	s1 := rows.signature()
	rows.add(row("a", 11))
	assert.NotEqual(t, s1, rows.signature())
	rows.add(row("a", 10))
	assert.Equal(t, s1, rows.signature(), "the same content gives the same signature")
	rows.remove("a")
	assert.NotEqual(t, s1, rows.signature())
}

// With the budget full, a row that REPLACES a held one with the same amount of content must still be taken (its old size is given back first), and one that
// would not fit leaves the old row in place.
// Mutation: count the replaced row's old size and the new one together against the budget: a machine at its budget can no longer update its own sessions.
func TestRemoteRowsAFullBudgetStillTakesAReplacementThatFits(t *testing.T) {
	rows := newRemoteRows()
	row := func(id, content string, pad int) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"session_id":%q,"name":%q,"pad":%q}`, id, content, strings.Repeat("x", pad)))
	}
	n := 0
	for rows.add(row(fmt.Sprintf("s%d", n), "v1", 100<<10)) {
		n++
	}
	require.Greater(t, n, 100, "the budget took %d rows of 100 KiB", n)
	require.True(t, rows.add(row("s0", "v2", 100<<10)), "same size, other content: it fits once the old row's size is given back")
	assert.Equal(t, "v2", rows.rows["s0"].m["name"])
	headroom := maxRemoteBytes - rows.bytes // less than one 100 KiB row
	require.False(t, rows.add(row("s1", "bigger", 100<<10+headroom+100)), "more than the old row plus what is left of the budget: left out")
	assert.Equal(t, "v1", rows.rows["s1"].m["name"], "the old row stays")
	require.LessOrEqual(t, rows.bytes, maxRemoteBytes)
}
