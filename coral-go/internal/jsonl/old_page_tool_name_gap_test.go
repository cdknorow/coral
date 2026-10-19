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

// A tool call, then fillMiB of other messages, then its result: does a page that starts at the result still name its tool?
func TestOldPageNamesItsToolAcrossAGap(t *testing.T) {
	for _, gapKiB := range []int{200, 900, 1500, 2500, 3500, 6000} {
		t.Run(fmt.Sprint(gapKiB, "KiB"), func(t *testing.T) {
			defer SetTranscriptBoundsForTest(40, 1<<30, 8<<20, 4096)()
			dir := t.TempDir()
			t.Setenv("CLAUDE_PROJECTS_DIR", dir)
			sid := "00000000-0000-4000-8000-000000005059"
			path := filepath.Join(dir, "proj", sid+".jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			var b strings.Builder
			ts := func(i int) string { return fmt.Sprintf("2026-10-19T10:%02d:%02dZ", (i/60)%60, i%60) }
			for i := 0; i < 30; i++ {
				fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"role":"user","content":"early %d"}}`+"\n", ts(i), i)
			}
			fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_GAP","name":"Read","input":{"file_path":"/x"}}]}}`+"\n", ts(31))
			filler := strings.Repeat("f", 50*1024)
			for i := 0; i < gapKiB/50; i++ {
				fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"role":"user","content":"filler %d %s"}}`+"\n", ts(40+i), i, filler)
			}
			fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_GAP","content":"the result"}]}}`+"\n", ts(2000))
			for i := 0; i < 80; i++ {
				fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"role":"user","content":"late %d"}}`+"\n", ts(2100+i), i)
			}
			require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
			want := referenceMessages(t, path, "claude")
			r := NewSessionReader()
			ctx := context.Background()
			tv := r.ReadTranscriptContext(ctx, sid, "", "claude")
			require.Equal(t, len(want), tv.Total)
			idx := -1
			for i, m := range want {
				if m["type"] == "tool_result" {
					idx = i
				}
			}
			require.Greater(t, idx, 0)
			require.Less(t, idx, tv.Base, "the result is before the window")
			got := tv.Slice(ctx, idx, idx+1)
			require.Len(t, got, 1)
			require.Equal(t, want[idx]["tool_name"], got[0]["tool_name"], "the page that starts at the result names its tool as the whole-file parse does (gap %d KiB)", gapKiB)
		})
	}
}
