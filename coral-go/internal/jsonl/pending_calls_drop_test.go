package jsonl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A pending call the bound on the tool names would drop before any checkpoint has seen it is still carried (Frontend's probe, #5104): a call left unanswered, placed after k filler pairs, answered much later; with the tool-name map bounded far below the calls a chunk holds.
func TestPendingCallDroppedByTheNameBoundIsStillCarried(t *testing.T) {
	defer SetTranscriptBoundsForTest(40, 1<<30, 64<<10, 30)()
	defer func(old int64) { pageChunkCap = old }(pageChunkCap)
	pageChunkCap = 4 << 10
	bad, checked := 0, 0
	for k := 150; k < 330; k += 1 {
		dir := t.TempDir()
		t.Setenv("CLAUDE_PROJECTS_DIR", dir)
		sid := fmt.Sprintf("00000000-0000-4000-8000-%012d", k)
		path := filepath.Join(dir, "proj", sid+".jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		var b strings.Builder
		ts := func(i int) string { return fmt.Sprintf("2026-10-19T10:%02d:%02dZ", (i/60)%60, i%60) }
		pad := strings.Repeat("p", 120)
		pair := func(i int) {
			fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_%04d","name":"Read","input":{"file_path":"/x"}}]}}`+"\n", ts(i), i)
			fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%04d","content":"ok %s"}]}}`+"\n", ts(i), i, pad)
		}
		for i := 0; i < k; i++ {
			pair(i)
		}
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_LONG","name":"Task","input":{"prompt":"x"}}]}}`+"\n", ts(1))
		for i := 1000; i < 1000+30; i++ {
			pair(i)
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
		if idx < 0 || idx >= tv.Base {
			continue
		}
		checked++
		got := tv.Slice(ctx, idx, idx+1)
		if len(got) != 1 || got[0]["tool_name"] != "Task" {
			bad++
			t.Errorf("k=%d: tool_name %v (want Task)", k, got)
		}
	}
	require.Zero(t, bad, "positions that lost the tool name")
	require.Positive(t, checked, "the probe reached the result before the window")
}
