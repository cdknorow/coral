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

// referenceMessages is the transcript read the way the reader did before the window (#4620): the
// whole file at once, one parser, no bounds. The windowed reader must give the same messages.
func referenceMessages(t *testing.T, path, agentType string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	data := completeLines(raw, true)
	entries, err := decodeLimited(context.Background(), data)
	require.NoError(t, err)
	p := &entryParser{agentType: agentType, names: map[string]string{}}
	return p.parse(entries)
}

func claudeFixture(turns int) string {
	var b strings.Builder
	for i := 0; i < turns; i++ {
		fmt.Fprintf(&b, `{"type":"user","timestamp":"2026-10-18T10:%02d:%02dZ","message":{"content":"question %d with some text"}}`+"\n", i/60, i%60, i)
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":"2026-10-18T10:%02d:%02dZ","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"answer %d"},{"type":"tool_use","id":"tu_%d","name":"Read","input":{"file_path":"/repo/f%d.go"}}]}}`+"\n", i/60, i%60, i, i, i)
		fmt.Fprintf(&b, `{"type":"user","timestamp":"2026-10-18T10:%02d:%02dZ","message":{"content":[{"type":"tool_result","tool_use_id":"tu_%d","content":"contents of f%d"}]}}`+"\n", i/60, i%60, i, i)
	}
	return b.String()
}

func codexFixture(turns int) string {
	var b strings.Builder
	for i := 0; i < turns; i++ {
		ts := fmt.Sprintf("2026-10-18T10:%02d:%02d.000Z", i/60, i%60)
		// The same user text twice (an event and a response item): merged into one message.
		fmt.Fprintf(&b, `{"timestamp":%q,"type":"event_msg","payload":{"type":"user_message","message":"codex question %d"}}`+"\n", ts, i)
		fmt.Fprintf(&b, `{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"codex question %d"}]}}`+"\n", ts, i)
		fmt.Fprintf(&b, `{"timestamp":%q,"type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"call_%d","arguments":"{\"command\":[\"ls\"]}"}}`+"\n", ts, i)
		fmt.Fprintf(&b, `{"timestamp":%q,"type":"response_item","payload":{"type":"function_call_output","call_id":"call_%d","output":"{\"output\":\"file%d\"}"}}`+"\n", ts, i, i)
	}
	return b.String()
}

func writeWindowFixture(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func appendTo(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func assertSameMessages(t *testing.T, want, got []map[string]any, what string) {
	t.Helper()
	require.Len(t, got, len(want), what)
	for i := range want {
		require.Equal(t, want[i], got[i], "%s: message %d", what, i)
	}
}

// With a small window and small chunks, the transcript reads as it did without them: the same
// total, every message the same (the window plus what is read back from the file), a Slice or
// Each over any range the same, and so after more is appended, a line at a time and half a line.
func TestWindowedReaderMatchesTheUnboundedReader(t *testing.T) {
	for _, tc := range []struct {
		name, agentType string
		fixture         func(int) string
	}{{"claude", "claude", claudeFixture}, {"codex", "codex", codexFixture}} {
		t.Run(tc.name, func(t *testing.T) {
			defer SetTranscriptBoundsForTest(40, 1<<30, 700, 8)() // 40 messages, chunks of 700 bytes (a few lines), 8 tool names
			dir := t.TempDir()
			sid := "00000000-0000-4000-8000-000000004620"
			var path string
			if tc.agentType == "claude" {
				t.Setenv("CLAUDE_PROJECTS_DIR", dir)
				path = filepath.Join(dir, "proj", sid+".jsonl")
			} else {
				home := filepath.Join(dir, ".codex")
				t.Setenv("CODEX_HOME", home)
				path = filepath.Join(home, "sessions", "2026", "10", "18", "rollout-2026-10-18T10-00-00-"+sid+".jsonl")
			}
			writeWindowFixture(t, path, tc.fixture(60))
			ctx := context.Background()
			r := NewSessionReader()

			check := func(what string) *Transcript {
				t.Helper()
				want := referenceMessages(t, path, tc.agentType)
				tv := r.ReadTranscriptContext(ctx, sid, "", tc.agentType)
				require.Equal(t, len(want), tv.Total, what)
				assert.LessOrEqual(t, len(tv.Window), 40, what+": the window is bounded")
				assert.Equal(t, tv.Total-len(tv.Window), tv.Base, what)
				assertSameMessages(t, want[tv.Base:], tv.Window, what+": window")
				assertSameMessages(t, want, tv.All(ctx), what+": all")
				for _, rg := range [][2]int{{0, 5}, {3, tv.Base}, {tv.Base - 2, tv.Base + 3}, {tv.Base, tv.Total}, {tv.Total - 1, tv.Total + 9}, {0, tv.Total}} {
					from, to := max(rg[0], 0), min(rg[1], tv.Total)
					if from >= to {
						continue
					}
					assertSameMessages(t, want[from:to], tv.Slice(ctx, from, to), fmt.Sprintf("%s: slice %v", what, rg))
				}
				var seen []int
				tv.Each(ctx, 2, tv.Base-1, func(i int, m map[string]any) bool {
					seen = append(seen, i)
					assert.Equal(t, want[i], m)
					return true
				})
				if tv.Base > 3 {
					require.NotEmpty(t, seen)
					assert.Equal(t, 2, seen[0])
					assert.Equal(t, tv.Base-2, seen[len(seen)-1])
				}
				return tv
			}

			tv := check("cold read")
			require.Greater(t, tv.Base, 0, "the fixture is larger than the window")
			if tc.agentType == "claude" {
				assert.LessOrEqual(t, len(r.cache[sid].toolUseNames), 8+2, "the tool names are capped")
			}
			// More lines arrive, then half a line, then the rest of it.
			appendTo(t, path, tc.fixture(5))
			check("after an append")
			cut := tc.fixture(1)
			appendTo(t, path, cut[:len(cut)/2])
			check("half a line is left unread")
			appendTo(t, path, cut[len(cut)/2:])
			check("and read when it is complete")
			// Polls that find nothing new.
			again := r.ReadTranscriptContext(ctx, sid, "", tc.agentType)
			assert.Equal(t, tv.Total+5*tvPerTurn(tc.agentType)+tvPerTurn(tc.agentType), again.Total)
		})
	}
}

// tvPerTurn is how many messages a fixture turn makes.
func tvPerTurn(agentType string) int {
	if agentType == "codex" {
		return 3 // the user text (merged), the call and its output
	}
	return 3
}

// The byte bound drops the oldest messages too, and always keeps the newest.
func TestWindowKeepsTheNewestWithinTheByteBound(t *testing.T) {
	defer SetTranscriptBoundsForTest(1000, 2000, 1<<20, 4096)()
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000004621"
	path := filepath.Join(dir, "proj", sid+".jsonl")
	writeWindowFixture(t, path, claudeFixture(30))
	r := NewSessionReader()
	tv := r.ReadTranscriptContext(context.Background(), sid, "", "claude")
	want := referenceMessages(t, path, "claude")
	require.Equal(t, len(want), tv.Total)
	require.Greater(t, tv.Base, 0)
	assert.LessOrEqual(t, r.cache[sid].msgBytes, 2000+approxSize(want[len(want)-1]))
	assertSameMessages(t, want[tv.Base:], tv.Window, "the newest messages")
	assertSameMessages(t, want, tv.All(context.Background()), "everything")

	// A single message bigger than the bound still stays: the window never empties.
	defer SetTranscriptBoundsForTest(1000, 10, 1<<20, 4096)()
	r2 := NewSessionReader()
	tv2 := r2.ReadTranscriptContext(context.Background(), sid, "", "claude")
	require.Len(t, tv2.Window, 1)
	assertSameMessages(t, want[len(want)-1:], tv2.Window, "the newest alone")
}

// An ancestor's transcript comes first in the numbering, and the pages across the join read the same.
func TestWindowedLineageNumbersAncestorFirst(t *testing.T) {
	defer SetTranscriptBoundsForTest(20, 1<<30, 600, 4096)()
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	anc, cur := "00000000-0000-4000-8000-000000004622", "00000000-0000-4000-8000-000000004623"
	ancPath, curPath := filepath.Join(dir, "proj", anc+".jsonl"), filepath.Join(dir, "proj", cur+".jsonl")
	writeWindowFixture(t, ancPath, claudeFixture(15))
	writeWindowFixture(t, curPath, strings.ReplaceAll(claudeFixture(12), "question", "later question"))
	r := NewSessionReader()
	ctx := context.Background()
	tv := r.ReadTranscriptForLiveWithLineageContext(ctx, cur, "", "claude", anc)
	want := append(referenceMessages(t, ancPath, "claude"), referenceMessages(t, curPath, "claude")...)
	require.Equal(t, len(want), tv.Total)
	require.Greater(t, tv.Base, 15*3-20, "the window is the current transcript's newest")
	assertSameMessages(t, want, tv.All(ctx), "ancestor then current")
	for _, rg := range [][2]int{{0, 10}, {40, 60}, {tv.Base - 3, tv.Base + 4}, {len(want) - 6, len(want)}} {
		assertSameMessages(t, want[rg[0]:rg[1]], tv.Slice(ctx, rg[0], rg[1]), fmt.Sprint(rg))
	}
	// The same through the list API the history callers use.
	msgs, total := r.ReadAllMessagesForLiveWithLineageContext(ctx, cur, "", "claude", anc)
	assert.Equal(t, len(want), total)
	assertSameMessages(t, want, msgs, "ReadAllMessagesForLiveWithLineageContext")
}

// The read is in chunks: a line longer than a chunk is read whole, and a file much larger than a chunk
// is consumed over several passes of one call.
func TestChunkedReadHandlesALineLongerThanAChunk(t *testing.T) {
	defer SetTranscriptBoundsForTest(5000, 1<<30, 256, 4096)()
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000004624"
	path := filepath.Join(dir, "proj", sid+".jsonl")
	huge := strings.Repeat("x", 5000)
	content := claudeFixture(3) + fmt.Sprintf(`{"type":"user","timestamp":"2026-10-18T11:00:00Z","message":{"content":%q}}`+"\n", huge) + claudeFixture(3)
	writeWindowFixture(t, path, content)
	r := NewSessionReader()
	tv := r.ReadTranscriptContext(context.Background(), sid, "", "claude")
	want := referenceMessages(t, path, "claude")
	assertSameMessages(t, want, tv.All(context.Background()), "a line of 5000 bytes in chunks of 256")
	assert.EqualValues(t, len(content), r.cache[sid].offset, "everything was consumed")
}

// A transcript replaced by a shorter one is read again from the start, base included.
func TestReplacedTranscriptResetsTheWindow(t *testing.T) {
	defer SetTranscriptBoundsForTest(10, 1<<30, 1<<20, 4096)()
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000004625"
	path := filepath.Join(dir, "proj", sid+".jsonl")
	writeWindowFixture(t, path, claudeFixture(20))
	r := NewSessionReader()
	tv := r.ReadTranscriptContext(context.Background(), sid, "", "claude")
	require.Greater(t, tv.Base, 0)
	writeWindowFixture(t, path, strings.ReplaceAll(claudeFixture(2), "question", "fresh"))
	tv = r.ReadTranscriptContext(context.Background(), sid, "", "claude")
	assert.Equal(t, 0, tv.Base)
	assert.Equal(t, 6, tv.Total)
}

func TestBoundNamesDropsTheOldestChunksFirstAndKeepsAgyBookkeeping(t *testing.T) {
	defer SetTranscriptBoundsForTest(5000, 1<<30, 1<<20, 6)()
	names := map[string]string{"__last_call_id": "c0", "__last_tool_name": "Read"}
	var seen map[string]uint32
	var seq uint32
	for chunk := 0; chunk < 4; chunk++ {
		for i := 0; i < 3; i++ {
			names[fmt.Sprintf("id_%d_%d", chunk, i)] = "Bash"
		}
		seen, seq = boundNames(names, seen, seq)
	}
	// 12 ids were added; the cap is 6 names in all (the bookkeeping keys count): the two newest chunks
	// stay (6 ids), the bookkeeping stays, and the oldest chunks' ids are gone.
	assert.Contains(t, names, "__last_call_id")
	assert.Contains(t, names, "__last_tool_name")
	for i := 0; i < 3; i++ {
		assert.NotContains(t, names, fmt.Sprintf("id_0_%d", i))
		assert.NotContains(t, names, fmt.Sprintf("id_1_%d", i))
		assert.Contains(t, names, fmt.Sprintf("id_3_%d", i))
	}
	assert.LessOrEqual(t, len(names), 8)
}
