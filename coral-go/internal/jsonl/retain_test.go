package jsonl

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeClaudeTranscript(t testing.TB, dir, sessionID string, turns int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < turns; i++ {
		fmt.Fprintf(&b, `{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"content":"Please work on step %d of the task and report results with details"}}`+"\n", i)
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":"2026-01-01T00:00:01Z","message":{"content":[{"type":"text","text":"Working on step %d: reading files, running tests, summarising the output for the user"},{"type":"tool_use","id":"tu_%d","name":"Read","input":{"file_path":"/src/project/file_%d.go"}}]}}`+"\n", i, i, i)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRetainDropsEndedSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewSessionReader()
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("s-%d", i)
		writeClaudeTranscript(t, proj, id, 3)
		if _, n := r.ReadNewMessages(id, "", "claude"); n != 6 {
			t.Fatalf("%s: read %d messages, want 6", id, n)
		}
		r.FirstUserPrompt(id, "", "claude")
	}
	r.FirstUserPrompt("prompt-only", "", "claude") // first-prompt cache without parsed messages
	if got := r.CachedSessions(); got != 6 {
		t.Fatalf("cached sessions = %d, want 6", got)
	}

	live := map[string]bool{"s-1": true, "s-3": true}
	if dropped := r.Retain(func(id string) bool { return live[id] }); dropped != 4 {
		t.Fatalf("dropped %d, want 4", dropped)
	}
	if got := r.CachedSessions(); got != 2 {
		t.Fatalf("cached sessions after Retain = %d, want 2", got)
	}
	// A kept session keeps its offset: no messages are re-read.
	if msgs, n := r.ReadNewMessages("s-1", "", "claude"); len(msgs) != 0 || n != 6 {
		t.Fatalf("kept session re-read: %d new of %d", len(msgs), n)
	}
	// A dropped session that is read again is re-parsed in full.
	if msgs, n := r.ReadNewMessages("s-0", "", "claude"); len(msgs) != 6 || n != 6 {
		t.Fatalf("dropped session: %d new of %d, want 6 of 6", len(msgs), n)
	}
}

// TestRetainHeap reports the reader's heap before and after evicting ended
// sessions at a realistic scale (100 sessions, 2000 transcript messages each,
// 90 ended). Opt-in: CORAL_HEAPTEST=1.
func TestRetainHeap(t *testing.T) {
	if os.Getenv("CORAL_HEAPTEST") != "1" {
		t.Skip("set CORAL_HEAPTEST=1")
	}
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	heap := func() uint64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	base := heap()
	r := NewSessionReader()
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("sess-%03d", i)
		writeClaudeTranscript(t, proj, id, 1000)
		r.ReadNewMessages(id, "", "claude")
		r.FirstUserPrompt(id, "", "claude")
	}
	full := heap()
	r.Retain(func(id string) bool { return id < "sess-010" }) // 10 still live
	after := heap()
	t.Logf("SessionReader heap: baseline %.1fMB, 100 sessions cached %.1fMB, after evicting 90 ended %.1fMB (cached sessions %d)",
		float64(base)/(1<<20), float64(full)/(1<<20), float64(after)/(1<<20), r.CachedSessions())
	runtime.KeepAlive(r)
}
