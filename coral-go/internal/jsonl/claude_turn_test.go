package jsonl

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// #3481: Claude records the end of a turn in its transcript even when no hook fires (a
// denied permission prompt, an interrupt). Records shaped as Claude Code 2.1.300 writes
// them (ids and paths neutral).
func TestReadClaudeTurnEvent(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", base)
	sid := "00000000-0000-4000-8000-000000003481"
	dir := filepath.Join(base, "-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	write := func(lines ...string) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			f.WriteString(l + "\n")
		}
		f.Close()
	}
	r := NewSessionReader()

	write(`{"type":"user","message":{"role":"user","content":"Run the shell command: touch x"},"timestamp":"2026-10-15T00:22:10.000Z"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","id":"t1"}]},"timestamp":"2026-10-15T00:22:11.000Z"}`)
	if ev, _ := r.ReadClaudeTurnEvent(sid, "/work"); ev != "" {
		t.Fatalf("mid-turn: got %q", ev)
	}
	// A tool result that merely quotes the marker is not an interrupt.
	write(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"[Request interrupted by user] appears in a file","tool_use_id":"t0"}]},"timestamp":"2026-10-15T00:22:12.000Z"}`)
	if ev, _ := r.ReadClaudeTurnEvent(sid, "/work"); ev != "" {
		t.Fatalf("a quoted marker: got %q", ev)
	}
	// The user denied the prompt: a rejected tool result, the interrupt marker, then turn_duration.
	write(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"The user doesn't want to proceed with this tool use.","is_error":true,"tool_use_id":"t1"}]},"timestamp":"2026-10-15T00:22:16.582Z"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]},"timestamp":"2026-10-15T00:22:16.583Z"}`)
	ev, at := r.ReadClaudeTurnEvent(sid, "/work")
	if ev != "stop" || !at.Equal(time.Date(2026, 10, 15, 0, 22, 16, 583e6, time.UTC)) {
		t.Fatalf("after the interrupt marker: %q %v", ev, at)
	}
	write(`{"type":"system","subtype":"turn_duration","durationMs":1442,"timestamp":"2026-10-15T00:22:16.584Z"}`)
	if ev, at := r.ReadClaudeTurnEvent(sid, "/work"); ev != "stop" || at.Nanosecond() != 584e6 {
		t.Fatalf("after turn_duration: %q %v", ev, at)
	}
}
