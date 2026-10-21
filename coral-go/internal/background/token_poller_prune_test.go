package background

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// State for sessions that ended is dropped each pass; live sessions, sleeping
// ones included, keep theirs (it is what prevents re-counting).
func TestTokenPollerPruneEnded(t *testing.T) {
	p := NewTokenPoller(nil, nil, time.Minute)
	now := time.Now()
	for _, id := range []string{"live", "sleeping", "ended"} {
		p.sessionPaths[id] = "/t/" + id + ".jsonl"
		p.lastCumulative[id] = cumulativeTokens{Input: 1}
		p.lastEntryCount[id] = 3
		p.setMtime(id, "/t/"+id+".jsonl", now)
		p.setMtime(id, "/t/"+id+"/subagents/agent-1.jsonl", now)
	}
	p.pruneEnded([]store.LiveSession{{SessionID: "live"}, {SessionID: "sleeping", IsSleeping: 1}})

	for _, m := range []int{len(p.sessionPaths), len(p.lastCumulative), len(p.lastEntryCount)} {
		require.Equal(t, 2, m)
	}
	require.Len(t, p.lastMtime, 4)
	require.Len(t, p.mtimeOwner, 4)
	require.NotContains(t, p.lastEntryCount, "ended")
	require.NotContains(t, p.lastMtime, "/t/ended/subagents/agent-1.jsonl")
	require.Equal(t, 3, p.lastEntryCount["sleeping"])

	p.pruneEnded(nil)
	require.Empty(t, p.lastMtime)
	require.Empty(t, p.sessionPaths)
}
