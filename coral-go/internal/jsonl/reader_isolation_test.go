package jsonl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionReader_NativeAgyUUIDHistoryRemainsReadable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANTIGRAVITY_DATA_DIR", root)
	t.Setenv("GEMINI_TMP_DIR", t.TempDir())
	id := "c1d2e3f4-a5b6-7890-1234-567890abcdef"
	p := filepath.Join(root, id, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{\"step_index\":0,\"type\":\"USER_INPUT\",\"content\":\"native history\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	msgs, total := NewSessionReader().ReadNewMessages(id, "", "agy")
	if total != 1 || len(msgs) != 1 {
		t.Fatalf("native Antigravity UUID history lost: total=%d messages=%v", total, msgs)
	}
}

func TestSessionReader_StrictAliasesClearPoisonedCache(t *testing.T) {
	for _, agentType := range []string{"agy", "antigravity", "gemini"} {
		t.Run(agentType, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("ANTIGRAVITY_DATA_DIR", root)
			t.Setenv("GEMINI_TMP_DIR", t.TempDir())
			id := "fresh-explicit-id"
			p := filepath.Join(root, id, ".system_generated", "logs", "transcript.jsonl")
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{\"type\":\"USER_INPUT\",\"content\":\"wrong history\"}\n"), 0644); err != nil {
				t.Fatal(err)
			}
			r := NewSessionReader()
			r.cache[id] = &sessionCache{path: p, offset: 10, messages: []map[string]any{{"content": "wrong history"}}, toolUseNames: map[string]string{"old": "Read"}}
			msgs, total := r.ReadAllMessagesForLive(id, "", agentType)
			if total != 0 || len(msgs) != 0 {
				t.Fatalf("strict %s leaked cache: %v", agentType, msgs)
			}
		})
	}
}

func TestResolveAgyTranscript_StrictRejectsUnmarkedLegacy(t *testing.T) {
	t.Setenv("ANTIGRAVITY_DATA_DIR", t.TempDir())
	legacy := t.TempDir()
	t.Setenv("GEMINI_TMP_DIR", legacy)
	id := "fresh-explicit-id"
	p := filepath.Join(legacy, "project", "chats", id+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"messages":[{"content":"wrong"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if got := resolveAgyTranscriptStrict(id); got != "" {
		t.Fatalf("strict resolver accepted unmarked legacy path %s", got)
	}
}
