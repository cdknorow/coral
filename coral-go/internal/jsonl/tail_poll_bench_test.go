package jsonl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkTailPoll is what the live list does for every agent every time it is read: the reader is asked for
// a transcript that has gained a few lines (here a call, its result and a reply) since the last read, in a
// session that has made thousands of tool calls (the tool names held are at their bound).
func BenchmarkTailPoll(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000005026"
	path := filepath.Join(dir, "proj", sid+".jsonl")
	writeBigClaudeTranscript(b, filepath.Join(dir, "proj"), sid, 8<<20) // thousands of calls
	r := NewSessionReader()
	ctx := context.Background()
	r.ReadTranscriptContext(ctx, sid, "", "claude")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		id := fmt.Sprintf("tp_%d", i)
		ts := "2026-10-19T12:00:00.000Z"
		fmt.Fprintf(f, `{"type":"assistant","timestamp":%q,"message":{"model":"m","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"ls"}}]}}`+"\n", ts, id)
		fmt.Fprintf(f, `{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"done"}]}}`+"\n", ts, id)
		b.StartTimer()
		r.ReadTranscriptContext(ctx, sid, "", "claude")
	}
}
