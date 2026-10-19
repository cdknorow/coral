package jsonl

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func result(id string) []map[string]any {
	return []map[string]any{{"type": "tool_result", "tool_use_id": id}}
}

// A call with no result yet is carried however many calls with results come after it, within the bound;
// the calls that have had their result go first when there are too many.
func TestPendingCallsKeepTheUnansweredCallsWhenThereAreTooMany(t *testing.T) {
	var p pendingCalls
	names := map[string]string{"old": "Task"}
	p.observe(names, nil) // never answered
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("c%03d", i)
		names[id] = "Read"
		p.observe(names, nil)
		p.observe(names, result(id))
	}
	snap := p.snapshot()
	assert.LessOrEqual(t, len(snap), pendingCallsMax)
	assert.Equal(t, "Task", snap["old"], "the unanswered call stays")
	assert.Equal(t, "Read", snap["c199"], "and the newest answered ones fill what room is left")
	assert.NotContains(t, snap, "c000", "the oldest answered one is gone")
}

// Many unanswered calls: the newest are kept, bounded.
func TestPendingCallsBoundTheUnansweredToo(t *testing.T) {
	var p pendingCalls
	names := map[string]string{}
	for i := 0; i < 3*pendingCallsMax; i++ {
		names[fmt.Sprintf("c%03d", i)] = "Bash"
		p.observe(names, nil)
	}
	snap := p.snapshot()
	require.Len(t, snap, pendingCallsMax)
	assert.Contains(t, snap, fmt.Sprintf("c%03d", 3*pendingCallsMax-1))
	assert.NotContains(t, snap, "c000")
}

// The "__" entries (an Antigravity reply's last call) are carried as they stand, and are not counted
// against the bound.
func TestPendingCallsCarryTheLastCallEntries(t *testing.T) {
	var p pendingCalls
	names := map[string]string{"__last_call_id": "a1", "__last_tool_name": "run", "a1": "run"}
	p.observe(names, nil)
	names["__last_call_id"], names["a2"] = "a2", "run"
	p.observe(names, nil)
	snap := p.snapshot()
	assert.Equal(t, "a2", snap["__last_call_id"])
	assert.Equal(t, "run", snap["__last_tool_name"])
	assert.Equal(t, "run", snap["a1"])
}

func TestPendingCallsSnapshotIsACopyAndNilWhenEmpty(t *testing.T) {
	var p pendingCalls
	assert.Nil(t, p.snapshot())
	p.observe(map[string]string{"x": "Read"}, nil)
	snap := p.snapshot()
	snap["x"] = "changed"
	assert.Equal(t, "Read", p.snapshot()["x"])
}

// What the cache forgets, the tracker forgets: a replaced transcript starts it over.
func TestPendingCallsAreForgottenWhenTheFileIsReplaced(t *testing.T) {
	defer SetTranscriptBoundsForTest(10, 1<<30, 700, 4096)()
	r, sid, path, _ := checkpointRig(t, "claude", 60)
	r.ReadTranscriptContext(context.Background(), sid, "", "claude")
	require.NotEmpty(t, r.cache[sid].pending.known)
	writeWindowFixture(t, path, claudeFixture(1))
	r.ReadTranscriptContext(context.Background(), sid, "", "claude")
	assert.LessOrEqual(t, len(r.cache[sid].pending.known), 2)
}
