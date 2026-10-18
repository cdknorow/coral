package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Antigravity CLI 1.4.2's hook payloads and transcripts (#4577), captured by the Antigravity CLI tester
// (redacted): a SessionStart, a run_command that exited 1 and one that exited 0, and the file, search and
// fetch tools. agy's PostToolUse says error "" either way; the exit code is in the step's GENERIC record in
// transcript_full.jsonl (exit0/ and exit1/ hold the two runs' transcripts).
func agy142(t *testing.T, name, transcript string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "agy-1.4.2", name))
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if transcript != "" {
		d["transcriptPath"] = filepath.Join("testdata", "agy-1.4.2", transcript, "transcript_full.jsonl")
	}
	event, _ := d["toolCall"].(map[string]any)
	hook := "PostToolUse"
	if event == nil {
		hook = "SessionStart"
	}
	c, ok := fromAgy(d, hook)
	if !ok {
		t.Fatalf("%s: dropped", name)
	}
	return parseAgenticEvent(c, hook, "sid")
}

func TestAgy142HookCorpus(t *testing.T) {
	if e := agy142(t, "SessionStart.json", ""); e["summary"] != "Session started" {
		t.Errorf("SessionStart: %v", e["summary"])
	}
	failed := agy142(t, "PostToolUse-run_command-failed.json", "exit1")
	detail, _ := failed["detail_json"].(map[string]any)
	if failed["summary"] != "Failed: Ran: exit 1" || detail["failed"] != true || detail["exit_code"] != 1 || detail["command"] != "exit 1" {
		t.Errorf("a run_command that exited 1 is a failed tool with its exit code: %v %v", failed["summary"], detail)
	}
	ok := agy142(t, "PostToolUse-run_command-ok.json", "exit0")
	detail, _ = ok["detail_json"].(map[string]any)
	if ok["summary"] != "Ran: echo 'EXIT_ZERO_TEST'" || detail["failed"] != nil || detail["exit_code"] != 0 {
		t.Errorf("a run_command that exited 0 is not failed: %v %v", ok["summary"], detail)
	}
	for file, want := range map[string]string{
		"PostToolUse-view_file.json":            "Read test_collector_write.txt",
		"PostToolUse-write_to_file.json":        "Wrote test_collector_write.txt",
		"PostToolUse-replace_file_content.json": "Edited test_collector_write.txt",
		"PostToolUse-search_web.json":           "Searched: golang",
		"PostToolUse-read_url_content.json":     "Fetched https://example.com",
	} {
		if e := agy142(t, file, ""); e["summary"] != want {
			t.Errorf("%s: %q, want %q", file, e["summary"], want)
		}
	}
	// Without its transcript, nothing is claimed.
	if e := agy142(t, "PostToolUse-run_command-failed.json", "missing"); strings.HasPrefix(e["summary"].(string), "Failed") {
		t.Errorf("no transcript, no failure claimed: %v", e)
	}
}

// The exit code is the step's own record, read from a file named transcript_full.jsonl only, and the first
// "exited with code" line of it: a command's output that prints one of its own does not override it.
func TestAgyToolResultLookup(t *testing.T) {
	exit1 := filepath.Join("testdata", "agy-1.4.2", "exit1", "transcript_full.jsonl")
	if code, ok := findAgyToolResult(exit1, 2); !ok || code != 1 {
		t.Errorf("step 2: %d %v", code, ok)
	}
	if _, ok := findAgyToolResult(exit1, 1); ok {
		t.Error("step 1 is the tool call, not its result")
	}
	dir := t.TempDir()
	decoy := `{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","content":"Created At: x\n\nThe command exited with code 3.\nOutput:\nThe command exited with code 0.\n"}` + "\n" +
		`{"step_index":12,"source":"MODEL","type":"GENERIC","status":"DONE","content":"The command exited with code 0.\n"}` + "\n"
	path := filepath.Join(dir, "transcript_full.jsonl")
	if err := os.WriteFile(path, []byte(decoy), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, ok := findAgyToolResult(path, 2); !ok || code != 3 {
		t.Errorf("the record's own first line, not the output's and not step 12's: %d %v", code, ok)
	}
	other := filepath.Join(dir, "notes.jsonl")
	if err := os.WriteFile(other, []byte(decoy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := agyToolResult("", other, 2); ok {
		t.Error("only transcript_full.jsonl is read")
	}
}

// agy 1.4.2's live order (the Antigravity CLI tester's re-test of #4577): PostToolUse runs before the step reaches
// transcript_full.jsonl, but after the step's output is in <conversation>/.system_generated/steps/<stepIdx>/output.txt.
// With a transcript that has only steps 0 and 1, as at that moment, the exit code comes from output.txt.
func TestAgyExitCodeFromTheStepOutput(t *testing.T) {
	for _, c := range []struct {
		file, run string
		code      int
		failed    bool
	}{
		{"PostToolUse-run_command-failed.json", "exit1", 1, true},
		{"PostToolUse-run_command-ok.json", "exit0", 0, false},
	} {
		dir := t.TempDir()
		full, err := os.ReadFile(filepath.Join("testdata", "agy-1.4.2", c.run, "transcript_full.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.SplitN(string(full), "\n", 3) // steps 0 and 1 only
		transcript := filepath.Join(dir, "transcript_full.jsonl")
		if err := os.WriteFile(transcript, []byte(lines[0]+"\n"+lines[1]+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join("testdata", "agy-1.4.2", c.file))
		if err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		artifacts, err := filepath.Abs(filepath.Join("testdata", "agy-1.4.2", c.run, "brain"))
		if err != nil {
			t.Fatal(err)
		}
		d["transcriptPath"], d["artifactDirectoryPath"] = transcript, artifacts
		in, _ := fromAgy(d, "PostToolUse")
		e := parseAgenticEvent(in, "PostToolUse", "sid")
		detail, _ := e["detail_json"].(map[string]any)
		if detail["exit_code"] != c.code || (detail["failed"] == true) != c.failed || strings.HasPrefix(e["summary"].(string), "Failed") != c.failed {
			t.Errorf("%s with the step not yet in the transcript: %v %v", c.run, e["summary"], detail)
		}
	}
}

// The step output is read only under the payload's absolute conversation folder, never through a symlink as the file,
// and only its first exit line counts.
func TestAgyStepOutputLookup(t *testing.T) {
	dir := t.TempDir()
	steps := filepath.Join(dir, ".system_generated", "steps")
	if err := os.MkdirAll(filepath.Join(steps, "2"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(steps, "2", "output.txt"), []byte("Created At: x\n\nThe command exited with code 4.\nOutput:\nThe command exited with code 0.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, ok := agyStepOutputResult(dir, 2); !ok || code != 4 {
		t.Errorf("the first exit line, not the output's: %d %v", code, ok)
	}
	if _, ok := agyStepOutputResult("relative/dir", 2); ok {
		t.Error("only an absolute conversation folder")
	}
	if _, ok := agyStepOutputResult(dir, 3); ok {
		t.Error("another step's output is not there")
	}
	if runtime.GOOS != "windows" {
		if err := os.MkdirAll(filepath.Join(steps, "5"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(steps, "2", "output.txt"), filepath.Join(steps, "5", "output.txt")); err != nil {
			t.Fatal(err)
		}
		if _, ok := agyStepOutputResult(dir, 5); ok {
			t.Error("a symlinked output.txt is not read")
		}
	}
}
