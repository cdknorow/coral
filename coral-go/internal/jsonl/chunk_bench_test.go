package jsonl

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func BenchmarkNextChunk(b *testing.B) {
	path := filepath.Join(b.TempDir(), "big.jsonl")
	require.NoError(b, os.WriteFile(path, bytes.Repeat([]byte(`{"type":"user","message":"hello"}`+"\n"), 1<<18), 0o600)) // 8.5 MB
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f, err := os.Open(path)
		require.NoError(b, err)
		chunk, _, err := nextChunk(io.LimitReader(f, 7<<20))
		f.Close()
		require.NoError(b, err)
		require.Equal(b, 7<<20, len(chunk))
	}
}

// BenchmarkOlderChatPage is what a chat paging back through its transcript costs: a 24 MB transcript,
// the newest messages in the window, and the oldest page read from the file (streamPart).
func BenchmarkOlderChatPage(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000005023"
	writeBigClaudeTranscript(b, filepath.Join(dir, "proj"), sid, 24<<20)
	r := NewSessionReader()
	ctx := context.Background()
	tv := r.ReadTranscriptContext(ctx, sid, "", "claude")
	require.Greater(b, tv.Base, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := tv.Slice(ctx, tv.Base/2, tv.Base/2+40)
		require.Len(b, page, 40)
	}
}

// BenchmarkColdTranscriptRead is a restart or an eviction: a fresh reader reads a 24 MB transcript from
// its start into a window (what every live session's first poll does).
func BenchmarkColdTranscriptRead(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000005025"
	writeBigClaudeTranscript(b, filepath.Join(dir, "proj"), sid, 24<<20)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tv := NewSessionReader().ReadTranscriptContext(ctx, sid, "", "claude")
		require.Greater(b, tv.Total, 1000)
	}
}
