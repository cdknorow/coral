//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// #3601 (weak site W21): the history indexers read only regular files. A symlink in an agent's
// history folder pointing at another file is skipped (its text never indexed), and a FIFO there
// doesn't stall the indexer.
func TestIndexersSkipLinksAndFIFOs(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte(`{"type":"user","message":{"content":"OUTSIDE-SECRET"},"timestamp":"2026-08-28T00:00:00Z"}
{"role":"user","content":"OUTSIDE-SECRET","timestamp":"2026-08-28T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		agentType, real, link, fifo string
	}{
		{"claude", "project-a/sess-claude.jsonl", "project-a/link.jsonl", "project-a/fifo.jsonl"},
		{"codex", "rollout-sess-codex.jsonl", "rollout-link.jsonl", "rollout-fifo.jsonl"},
		{"pi", "sess-pi.jsonl", "link.jsonl", "fifo.jsonl"},
		{"gemini", "uuid-1/chats/session-g1.json", "uuid-1/chats/session-link.json", "uuid-1/chats/session-fifo.json"},
		{"agy", "uuid-agy/.system_generated/logs/transcript.jsonl", "uuid-link/.system_generated/logs/transcript.jsonl", "uuid-fifo/.system_generated/logs/transcript.jsonl"},
	} {
		t.Run(c.agentType, func(t *testing.T) {
			impl, ok := GetAgent(c.agentType).(HistoryScanner)
			if !ok {
				t.Skipf("%s does not scan history", c.agentType)
			}
			dir := t.TempDir()
			writeHistoryFile(t, dir, c.real, `{"type":"user","message":{"content":"real one"},"timestamp":"2026-08-28T00:00:00Z"}`)
			for _, p := range []string{c.link, c.fifo} {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, filepath.Join(dir, c.link)); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(filepath.Join(dir, c.fifo), 0o600); err != nil {
				t.Fatal(err)
			}
			done := make(chan []IndexedSession, 1)
			go func() {
				s, _ := impl.ExtractSessions(dir, nil)
				done <- s
			}()
			select {
			case sessions := <-done:
				for _, s := range sessions {
					if strings.Contains(s.FTSBody, "OUTSIDE-SECRET") || strings.Contains(s.SourceFile, "link") {
						t.Errorf("a symlinked history file was indexed: %+v", s.SourceFile)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the indexer blocked on a FIFO")
			}
		})
	}
}
