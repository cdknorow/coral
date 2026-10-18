package jsonl

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeBigClaudeTranscript writes about targetBytes of Claude-shaped JSONL (user turns, assistant text,
// tool calls and their results, some large) to dir/<sessionID>.jsonl and returns the number of lines.
func writeBigClaudeTranscript(tb testing.TB, dir, sessionID string, targetBytes int64) int {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, sessionID+".jsonl"))
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	defer w.Flush()
	var written int64
	lines := 0
	big := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 40) // ~1.8 KB
	for turn := 0; written < targetBytes; turn++ {
		emit := func(s string) {
			n, _ := w.WriteString(s + "\n")
			written += int64(n)
			lines++
		}
		ts := fmt.Sprintf("2026-10-18T%02d:%02d:%02d.000Z", (turn/3600)%24, (turn/60)%60, turn%60)
		emit(fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"Please do step %d of the task and report back with details."}}`, ts, turn))
		emit(fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"Working on step %d. %s"},{"type":"tool_use","id":"tu_%d","name":"Bash","input":{"command":"ls -la /repo/step%d","description":"list"}}]}}`, ts, turn, big, turn, turn))
		result := "ok"
		if turn%4 == 0 {
			result = strings.Repeat(big, 6) // a ~10 KB tool output now and then
		} else {
			result = big
		}
		emit(fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"tu_%d","content":%q}]}}`, ts, turn, result))
	}
	return lines
}

// TestTranscriptCacheMemory (opt-in: CORAL_MEMTEST=MB, e.g. 200) reads a few large synthetic
// transcripts (MB in all, in an isolated CLAUDE_PROJECTS_DIR under the test's temp folder) the way the
// live chat does and prints the heap the reader holds after a collection (#4620).
func TestTranscriptCacheMemory(t *testing.T) {
	mb := 0
	fmt.Sscanf(os.Getenv("CORAL_MEMTEST"), "%d", &mb)
	if mb <= 0 {
		t.Skip("set CORAL_MEMTEST=<total MB>")
	}
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	proj := filepath.Join(dir, "proj")
	const sessions = 3
	ids := make([]string, sessions)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", 4620+i)
		writeBigClaudeTranscript(t, proj, ids[i], int64(mb)<<20/sessions)
	}
	var base runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&base)
	r := NewSessionReader()
	totals := make([]int, sessions)
	for i, id := range ids {
		_, totals[i] = chatRead(r, id, "claude")
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	// A second pass: a poll that finds nothing new.
	for _, id := range ids {
		chatRead(r, id, "claude")
	}
	t.Logf("MEMTEST total_mb=%d sessions=%d messages=%v heap_before_mb=%.0f heap_after_mb=%.0f reader_holds_mb=%.0f peak_sys_mb=%.0f",
		mb, sessions, totals, float64(base.HeapAlloc)/(1<<20), float64(after.HeapAlloc)/(1<<20),
		float64(after.HeapAlloc-min(after.HeapAlloc, base.HeapAlloc))/(1<<20), float64(after.Sys)/(1<<20))
	_ = context.Background
}
