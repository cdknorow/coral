package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Codex CLI 0.155.1's hook payloads (#4545), captured from a real run against a local stand-in for the
// Responses API (so a turn ran with no account): a fresh launch's SessionStart, a shell command that exited 1
// and one that exited 0 (PostToolUse gives only the output, as a string), and the Stop. The rollout file
// holds the lines of the transcript those calls wrote (paths redacted; the second call's id is call_2: the
// stand-in reused call_1).
func codexCorpus(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-0.155.1", name))
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if p, _ := d["transcript_path"].(string); p != "" {
		d["transcript_path"] = filepath.Join("testdata", "codex-0.155.1", filepath.Base(p)) // the captured rollout
	}
	return d
}

func TestCodexHookCorpus(t *testing.T) {
	start := parseAgenticEvent(codexCorpus(t, "SessionStart.json"), "SessionStart", "sid")
	if start["event_type"] != "session_reset" || start["summary"] != "Session started" {
		t.Errorf("a fresh launch is a session start, not a /clear: %v", start)
	}

	failed := parseAgenticEvent(codexCorpus(t, "PostToolUse-failed.json"), "PostToolUse", "sid")
	detail, _ := failed["detail_json"].(map[string]any)
	if failed["event_type"] != "tool_use" || !strings.HasPrefix(failed["summary"].(string), "Failed: Ran: printf approved-x") {
		t.Errorf("a command that exited 1 is a failed tool: %v", failed)
	}
	if detail["failed"] != true || detail["exit_code"] != 1 {
		t.Errorf("its detail carries failed and the exit code: %v", detail)
	}
	if _, kept := detail["error"]; kept {
		t.Errorf("the command's output is not copied into the event: %v", detail)
	}

	ok := parseAgenticEvent(codexCorpus(t, "PostToolUse-ok.json"), "PostToolUse", "sid")
	detail, _ = ok["detail_json"].(map[string]any)
	if strings.HasPrefix(ok["summary"].(string), "Failed") || detail["failed"] != nil || detail["exit_code"] != 0 {
		t.Errorf("a command that exited 0 is not failed: %v", ok)
	}

	stop := parseAgenticEvent(codexCorpus(t, "Stop.json"), "Stop", "sid")
	if stop["event_type"] != "stop" {
		t.Errorf("stop: %v", stop)
	}
}

// The lookup reads only a Codex rollout file, and a call it can't find leaves the event as it was.
func TestCodexToolResultLookup(t *testing.T) {
	roll := filepath.Join("testdata", "codex-0.155.1", "rollout-2026-10-17T21-38-46-01a14d4e-1a66-7893-b33a-f0de799b1d64.jsonl")
	if code, status, ok := findCodexToolResult(roll, "call_1"); !ok || code != 1 || status != "failed" {
		t.Errorf("call_1: %d %q %v", code, status, ok)
	}
	if code, status, ok := findCodexToolResult(roll, "call_2"); !ok || code != 0 || status != "completed" {
		t.Errorf("call_2: %d %q %v", code, status, ok)
	}
	if _, _, ok := codexToolResult(roll, "call_9"); ok {
		t.Error("an unknown call is not found")
	}
	for _, path := range []string{"", filepath.Join("testdata", "codex-0.155.1", "Stop.json"), "/etc/passwd"} {
		if _, _, ok := codexToolResult(path, "call_1"); ok {
			t.Errorf("%q is not a rollout file", path)
		}
	}
	// The record is the call's own: a later record that only names the id (in another field) is not it.
	decoy := `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"call_7","status":"completed","parent_call":"call_1","exit_code":0}}}`
	withDecoy := filepath.Join(t.TempDir(), "rollout-decoy.jsonl")
	if err := os.WriteFile(withDecoy, append(mustRead(t, roll), []byte(decoy+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, ok := findCodexToolResult(withDecoy, "call_1"); !ok || code != 1 {
		t.Errorf("call_1 is found by its own record: %d %v", code, ok)
	}
	// A file that isn't a Codex rollout is never read, whatever it holds.
	rec := mustRead(t, roll)
	other := filepath.Join(t.TempDir(), "notes.jsonl")
	if err := os.WriteFile(other, rec, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := codexToolResult(other, "call_1"); ok {
		t.Error("only a rollout-*.jsonl file is read")
	}
	d := codexCorpus(t, "PostToolUse-failed.json")
	d["transcript_path"] = filepath.Join(t.TempDir(), "rollout-missing.jsonl")
	ev := parseAgenticEvent(d, "PostToolUse", "sid")
	if strings.HasPrefix(ev["summary"].(string), "Failed") {
		t.Errorf("without the transcript nothing is claimed: %v", ev)
	}
}

// A SessionStart's summary says what started the session, from its source.
func TestSessionStartSummary(t *testing.T) {
	for source, want := range map[string]string{
		"startup": "Session started", "resume": "Session resumed", "clear": "Session reset: /clear",
		"compact": "Session compacted", "fork": "Session forked", "": "Session reset: /clear",
	} {
		if got := sessionStartSummary(map[string]any{"source": source}); got != want {
			t.Errorf("%q: %q, want %q", source, got, want)
		}
	}
	if got := sessionStartSummary(map[string]any{"source": "startup", "_coral_session_clear": true}); got != "Session reset: /clear" {
		t.Errorf("Coral's /clear marker wins: %q", got)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Only a shell command has an exit code: another Codex tool call (its response a string too) never waits on
// the transcript lookup (Security's INFO on #4545).
func TestCodexLookupOnlyForShellTools(t *testing.T) {
	calls := 0
	orig := codexToolResult
	codexToolResult = func(path, callID string) (int, string, bool) { calls++; return orig(path, callID) }
	t.Cleanup(func() { codexToolResult = orig })
	d := codexCorpus(t, "PostToolUse-failed.json")
	d["tool_name"] = "apply_patch"
	d["tool_input"] = map[string]any{"input": "*** Begin Patch"}
	ev := parseAgenticEvent(d, "PostToolUse", "sid")
	if calls != 0 || strings.HasPrefix(ev["summary"].(string), "Failed") {
		t.Errorf("a non-shell tool looked up the transcript (%d) or was marked failed: %v", calls, ev)
	}
	parseAgenticEvent(codexCorpus(t, "PostToolUse-failed.json"), "PostToolUse", "sid")
	if calls != 1 {
		t.Errorf("the shell command is looked up once, got %d", calls)
	}
}

// The rollout path comes from the hook payload: a symlink named like a rollout is not followed.
func TestCodexLookupDoesNotFollowASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows; safefs checks the final name there with Lstat")
	}
	roll, err := filepath.Abs(filepath.Join("testdata", "codex-0.155.1", "rollout-2026-10-17T21-38-46-01a14d4e-1a66-7893-b33a-f0de799b1d64.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "rollout-link.jsonl")
	if err := os.Symlink(roll, link); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := findCodexToolResult(link, "call_1"); ok {
		t.Error("a symlinked rollout file was read")
	}
	if _, _, ok := findCodexToolResult(roll, "call_1"); !ok {
		t.Error("the file itself is read")
	}
}
