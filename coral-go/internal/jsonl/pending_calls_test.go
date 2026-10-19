package jsonl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	p.snapshot(names) // never answered
	var snap map[string]string
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("c%03d", i)
		names[id] = "Read"
		p.noteResults(result(id))
		snap = p.snapshot(names)
	}
	assert.LessOrEqual(t, len(snap), pendingCallsMax)
	assert.Equal(t, "Task", snap["old"], "the unanswered call stays")
	assert.Equal(t, "Read", snap["c199"], "and the newest answered ones fill what room is left")
	assert.NotContains(t, snap, "c000", "the oldest answered one is gone")
}

// Many unanswered calls: the newest are kept, bounded.
func TestPendingCallsBoundTheUnansweredToo(t *testing.T) {
	var p pendingCalls
	names := map[string]string{}
	var snap map[string]string
	for i := 0; i < 3*pendingCallsMax; i++ {
		names[fmt.Sprintf("c%03d", i)] = "Bash"
		snap = p.snapshot(names)
	}
	require.Len(t, snap, pendingCallsMax)
	assert.Contains(t, snap, fmt.Sprintf("c%03d", 3*pendingCallsMax-1))
	assert.NotContains(t, snap, "c000")
}

// The "__" entries (an Antigravity reply's last call) are carried as they stand, and are not counted
// against the bound.
func TestPendingCallsCarryTheLastCallEntries(t *testing.T) {
	var p pendingCalls
	names := map[string]string{"__last_call_id": "a1", "__last_tool_name": "run", "a1": "run"}
	p.snapshot(names)
	names["__last_call_id"], names["a2"] = "a2", "run"
	snap := p.snapshot(names)
	assert.Equal(t, "a2", snap["__last_call_id"])
	assert.Equal(t, "run", snap["__last_tool_name"])
	assert.Equal(t, "run", snap["a1"])
}

// A result noted between two snapshots marks a call the second snapshot first sees (a call and its result
// within one megabyte), and a call seen by an earlier snapshot.
func TestPendingCallsResultsMarkCallsSeenNowOrBefore(t *testing.T) {
	var p pendingCalls
	names := map[string]string{"early": "Read"}
	p.snapshot(names)
	for i := 0; i < pendingCallsMax; i++ { // enough unanswered calls that the answered ones are what is trimmed
		names[fmt.Sprintf("u%02d", i)] = "Bash"
	}
	names["late"] = "Read"
	p.noteResults(result("early"))
	p.noteResults(result("late"))
	snap := p.snapshot(names)
	assert.Len(t, snap, pendingCallsMax)
	assert.NotContains(t, snap, "early", "answered before this snapshot")
	assert.NotContains(t, snap, "late", "new and answered within it")
	for i := 0; i < pendingCallsMax; i++ {
		assert.Contains(t, snap, fmt.Sprintf("u%02d", i))
	}
}

func TestPendingCallsSnapshotIsACopyAndNilWhenEmpty(t *testing.T) {
	var p pendingCalls
	assert.Nil(t, p.snapshot(map[string]string{}))
	snap := p.snapshot(map[string]string{"x": "Read"})
	snap["x"] = "changed"
	assert.Equal(t, "Read", p.snapshot(map[string]string{"x": "Read"})["x"])
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

// A call still waiting for its result keeps its name through checkpoints even with far more than the bound of
// other calls, each answered, between it and its result: the page that starts at the result names its tool.
func TestOldPageNamesALongAnsweredCallAmongManyShortOnes(t *testing.T) {
	defer SetTranscriptBoundsForTest(40, 1<<30, 8<<20, 4096)()
	defer func(old int64) { pageChunkCap = old }(pageChunkCap)
	pageChunkCap = 4 << 10 // a checkpoint about every 2 KiB
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000005060"
	path := filepath.Join(dir, "proj", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	var b strings.Builder
	ts := func(i int) string { return fmt.Sprintf("2026-10-19T10:%02d:%02dZ", (i/60)%60, i%60) }
	pad := strings.Repeat("p", 300)
	fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_LONG","name":"Task","input":{"prompt":"x"}}]}}`+"\n", ts(1))
	for i := 0; i < 4*pendingCallsMax; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_%03d","name":"Read","input":{"file_path":"/x"}}]}}`+"\n", ts(2+i), i)
		fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%03d","content":"ok %s"}]}}`+"\n", ts(2+i), i, pad)
	}
	fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_LONG","content":"the long result"}]}}`+"\n", ts(3000))
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"role":"user","content":"late %d"}}`+"\n", ts(3100+i), i)
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
	want := referenceMessages(t, path, "claude")
	ctx := context.Background()
	tv := NewSessionReader().ReadTranscriptContext(ctx, sid, "", "claude")
	idx := -1
	for i, m := range want {
		if m["tool_use_id"] == "toolu_LONG" && m["type"] == "tool_result" {
			idx = i
		}
	}
	require.Greater(t, idx, 0)
	require.Less(t, idx, tv.Base)
	require.Equal(t, "Task", want[idx]["tool_name"])
	got := tv.Slice(ctx, idx, idx+1)
	require.Len(t, got, 1)
	assert.Equal(t, "Task", got[0]["tool_name"])
}

// The results noted for a snapshot are forgotten once it is taken.
func TestPendingCallsForgetTheResultsOfAnEarlierSnapshot(t *testing.T) {
	var p pendingCalls
	names := map[string]string{"a": "Read"}
	p.noteResults(result("a"))
	p.snapshot(names)
	assert.Empty(t, p.answered)
}
