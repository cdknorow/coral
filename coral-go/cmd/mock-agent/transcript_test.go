package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/jsonl"
)

// The mock's transcript is one Coral reads as Claude Code's own: each turn ends (turn_duration),
// the version is the CLI's, the messages parse (text, and a tool use with its result), and
// every assistant entry names its model.
func TestMockTranscriptIsReadLikeClaudes(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	sid := "00000000-0000-4000-8000-00000000a0c4"
	tr := newTranscript(sid, "claude-sonnet-5-5")
	if tr == nil {
		t.Fatal("no transcript")
	}
	for _, p := range []string{"first prompt", "second prompt\nwith a second line", "third"} {
		if err := tr.Turn(p); err != nil {
			t.Fatal(err)
		}
	}
	cwd, _ := os.Getwd()

	r := jsonl.NewSessionReader()
	if kind, _ := r.ReadClaudeTurnEvent(sid, cwd); kind != "stop" {
		t.Errorf("turn state %q, want stop (the turn_duration entry ends each turn)", kind)
	}
	if v := r.ReadClaudeVersion(sid, cwd); v != mockClaudeVersion {
		t.Errorf("version %q", v)
	}
	msgs, _ := r.ReadAllMessagesContext(context.Background(), sid, cwd, "claude")
	var texts, tools int
	for _, m := range msgs {
		if m["type"] != "assistant" {
			continue
		}
		texts++
		if uses, ok := m["tool_uses"].([]map[string]any); ok && len(uses) > 0 {
			tools++
		} else if uses, ok := m["tool_uses"].([]any); ok && len(uses) > 0 {
			tools++
		}
	}
	if texts < 3 || tools < 1 {
		t.Errorf("parsed %d assistant texts and %d tool uses from %d messages: %v", texts, tools, len(msgs), msgs)
	}

	f, err := os.Open(tr.path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	assistants := 0
	for sc.Scan() {
		var e struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			Message   struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("not JSON: %s", sc.Text())
		}
		if e.SessionID != sid {
			t.Errorf("sessionId %q", e.SessionID)
		}
		if e.Type == "assistant" {
			assistants++
			if e.Message.Model != "claude-sonnet-5-5" {
				t.Errorf("assistant model %q", e.Message.Model)
			}
		}
	}
	if assistants != 4 {
		t.Errorf("%d assistant entries, want 4 (three replies and one tool use)", assistants)
	}
}

// Without a session id, or with one that isn't a uuid (it names the file: "../x" must never
// reach outside the projects folder), there is no transcript, and Turn is a no-op.
func TestNoTranscriptWithoutASession(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	for _, sid := range []string{"", "../escape", "a/b", "00000000-0000-4000-8000-00000000a0c4/../x"} {
		var tr *transcript = newTranscript(sid, "")
		if tr != nil {
			t.Fatalf("a transcript for session id %q", sid)
		}
		if err := tr.Turn("x"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEncodeProjectDir(t *testing.T) {
	if got := encodeProjectDir("/srv/a.b/my repo"); got != "-srv-a-b-my-repo" {
		t.Errorf("got %q", got)
	}
}

// MOCK_AGENT_REPLY_DELAY: the prompt is in the transcript at once, as Claude records it, and the reply only after the delay (a slow first answer).
func TestReplyDelayKeepsThePromptImmediate(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	t.Setenv("MOCK_AGENT_REPLY_DELAY", "0.4")
	tr := newTranscript("00000000-0000-4000-8000-00000000a0c5", "claude-sonnet-5-5")
	if tr == nil {
		t.Fatal("no transcript")
	}
	count := func() int {
		b, err := os.ReadFile(tr.path)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "\n")
	}
	done := make(chan error, 1)
	go func() { done <- tr.Turn("hello") }()
	deadline := time.Now().Add(300 * time.Millisecond)
	for count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := count(); n != 1 {
		t.Fatalf("before the delay the transcript has %d lines, want just the prompt", n)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := count(); n < 3 {
		t.Fatalf("after the delay the transcript has %d lines, want the reply too", n)
	}
	t.Setenv("MOCK_AGENT_REPLY_DELAY", "nonsense")
	if replyDelay() != 0 {
		t.Fatal("a delay that is not a number is no delay")
	}
}
