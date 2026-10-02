package jsonl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/transcriptlink"
)

func TestCodexLinkReplacesCachedTranscriptAndResumeIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sid := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	thread := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	dir := filepath.Join(home, "sessions", "2026", "10", "02")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "rollout-old-"+sid+".jsonl")
	if err := os.WriteFile(old, []byte(`{"type":"event_msg","payload":{"type":"user_message","message":"old conversation"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewSessionReader()
	if _, n := r.ReadAllMessagesForLive(sid, "", "codex"); n != 1 {
		t.Fatalf("old count=%d", n)
	}
	if got := r.FirstUserPrompt(sid, "", "codex"); got != "old conversation" {
		t.Fatal(got)
	}
	p := filepath.Join(dir, "rollout-new-"+thread+".jsonl")
	content := fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n", thread) + `{"type":"event_msg","payload":{"type":"user_message","message":"current conversation"}}` + "\n" + `{"timestamp":"2026-10-02T01:00:00Z","type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := transcriptlink.BindCodex(sid, thread); err != nil {
		t.Fatal(err)
	}
	// Lifecycle refresh can see the mapping before the chat reader does.
	r.ReadCodexTurnEvent(sid, "")
	msgs, n := r.ReadAllMessagesForLive(sid, "", "codex")
	if n != 1 || msgs[0]["content"] != "current conversation" {
		t.Fatalf("stale chat: %v", msgs)
	}
	if got := r.FirstUserPrompt(sid, "", "codex"); got != "current conversation" {
		t.Fatal(got)
	}
	if _, n := NewSessionReader().ReadAllMessagesForLive(sid, "", "codex"); n != 1 {
		t.Fatalf("cold restart count=%d", n)
	}
	cmd := (&agent.CodexAgent{}).BuildLaunchCommand(agent.LaunchParams{ResumeSessionID: sid})
	if !strings.Contains(cmd, "resume "+thread) {
		t.Fatalf("wrong resume: %s", cmd)
	}
	if err := transcriptlink.BindCodex(sid, "../../other"); err == nil {
		t.Fatal("accepted invalid identity")
	}
	// A filename alone is insufficient evidence for an explicit association.
	fake := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	if err := os.WriteFile(filepath.Join(dir, "rollout-fake-"+fake+".jsonl"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := transcriptlink.BindCodex(sid, fake); err == nil {
		t.Fatal("accepted mismatched metadata")
	}
	if got := transcriptlink.Codex(sid); got.ThreadID != thread {
		t.Fatalf("failed binding replaced valid link: %+v", got)
	}
}
