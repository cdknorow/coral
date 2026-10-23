package jsonl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4969: the slash commands a Claude session ran, as its transcript records them (system entries of subtype local_command).
func TestReadClaudeLocalCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000004971"
	path := filepath.Join(dir, "x", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	cmd := func(ts, name, args string) string {
		return `{"type":"system","subtype":"local_command","timestamp":"` + ts + `","content":"<command-name>` + name + `</command-name>\n <command-message>x</command-message>\n <command-args>` + args + `</command-args>"}`
	}
	out := func(ts, text string) string {
		return `{"type":"system","subtype":"local_command","timestamp":"` + ts + `","content":"<local-command-stdout>` + text + `</local-command-stdout>"}`
	}
	appendLines(t, path,
		`{"type":"user","timestamp":"2026-10-19T00:00:00Z","message":{"role":"user","content":"<command-name>/model</command-name> typed by the user as text"}}`,
		cmd("2026-10-19T00:00:01Z", "/model", "sonnet"),
		out("2026-10-19T00:00:01Z", "Set model to `Sonnet 5.5` and saved as your default"),
		`{"type":"assistant","message":{"model":"claude-sonnet-5-5","content":[]},"timestamp":"2026-10-19T00:00:02Z"}`,
		cmd("2026-10-19T00:01:00Z", "/effort", "high"),
		out("2026-10-19T00:01:00Z", "Effort\\u001b[31m level\\u0007 set   to `high`"),
		cmd("2026-10-19T00:02:00Z", "/model", ""),
	)
	r := NewSessionReader()
	got := r.ReadClaudeLocalCommands(sid, "", time.Time{})
	require.Len(t, got, 3, "user text and assistant entries are not commands")
	assert.Equal(t, "/model", got[0].Name)
	assert.Equal(t, "sonnet", got[0].Args)
	assert.Equal(t, "Set model to Sonnet 5.5 and saved as your default", got[0].Output, "the output, without formatting backticks")
	assert.Equal(t, "/effort", got[1].Name)
	assert.Equal(t, "Effort[31m level set to high", strings.ReplaceAll(got[1].Output, "\x1b", ""), "control characters are removed")
	assert.NotContains(t, got[1].Output, "\x1b")
	assert.NotContains(t, got[1].Output, "\x07")
	assert.Equal(t, "", got[2].Output, "a command whose output is not written yet has none")

	since := time.Date(2026, 10, 19, 0, 0, 30, 0, time.UTC)
	got = r.ReadClaudeLocalCommands(sid, "", since)
	require.Len(t, got, 2)
	assert.Equal(t, "/effort", got[0].Name, "only commands at or after since")

	assert.Nil(t, r.ReadClaudeLocalCommands("00000000-0000-4000-8000-0000000049ff", "", time.Time{}), "no transcript: none")
}

// Only the end of a long transcript is searched; a command just run is there.
func TestReadClaudeLocalCommandsReadsTheEnd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000004972"
	path := filepath.Join(dir, "x", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	filler := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", 4000) + `"},"timestamp":"2026-10-19T00:00:00Z"}`
	var lines []string
	lines = append(lines, `{"type":"system","subtype":"local_command","timestamp":"2026-10-19T00:00:01Z","content":"<command-name>/model</command-name><command-args>old</command-args>"}`)
	for i := 0; i < localCommandTail/len(filler)+20; i++ {
		lines = append(lines, filler)
	}
	lines = append(lines,
		`{"type":"system","subtype":"local_command","timestamp":"2026-10-19T01:00:00Z","content":"<command-name>/model</command-name><command-args>new</command-args>"}`,
		`{"type":"system","subtype":"local_command","timestamp":"2026-10-19T01:00:00Z","content":"<local-command-stdout>Set model to New</local-command-stdout>"}`)
	appendLines(t, path, lines...)
	got := NewSessionReader().ReadClaudeLocalCommands(sid, "", time.Time{})
	require.Len(t, got, 1, "the old command is before the searched end")
	assert.Equal(t, "new", got[0].Args)
	assert.Equal(t, "Set model to New", got[0].Output)
}

func TestCleanCommandText(t *testing.T) {
	assert.Equal(t, "a b c", cleanCommandText("  a \n\t b\x00  c ", 50))
	assert.Equal(t, "abcde", cleanCommandText("abcdefgh", 5))
	assert.Equal(t, "Set model to X", cleanCommandText("Set model to `X`", 50))
}

// #6219: Claude Code 2.1.309 writes a slash command and its output as type "user" entries whose message.content is the command's tags, not as system entries of subtype local_command, and every /model
// and /effort switch waited for an entry that was never recognised. The records in testdata are real ones from 2.1.309 transcripts (a machine path replaced), one command and one output per /effort level.
func TestReadClaudeLocalCommandsFromClaude2_1_309(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "claude_2_1_309_effort_commands.jsonl"))
	require.NoError(t, err)
	got := parseLocalCommands(data, time.Time{})
	require.Len(t, got, 5)
	want := []struct{ args, output string }{
		{"low", "Set effort level to low"},
		{"high", "Set effort level to high"},
		{"xhigh", "Set effort level to xhigh"},
		{"max", "Set effort level to max"},
		{"auto", "Effort level set to auto"},
	}
	for i, w := range want {
		assert.Equal(t, "/effort", got[i].Name)
		assert.Equal(t, w.args, got[i].Args)
		assert.True(t, strings.HasPrefix(got[i].Output, w.output), "%s: %q", w.args, got[i].Output)
		assert.False(t, got[i].At.IsZero())
	}
}

// Both formats, for /model as well (no real /model record was captured: these have the shape of the real /effort ones), set and refused; and what is not the command's own entry is not a command.
func TestLocalCommandsInBothFormats(t *testing.T) {
	user := func(ts, content string) string {
		b, _ := json.Marshal(map[string]any{"type": "user", "timestamp": ts, "message": map[string]any{"role": "user", "content": content}})
		return string(b)
	}
	system := func(ts, content string) string {
		b, _ := json.Marshal(map[string]any{"type": "system", "subtype": "local_command", "timestamp": ts, "content": content})
		return string(b)
	}
	cmd := func(name, args string) string {
		return "<command-name>" + name + "</command-name>\n            <command-message>" + strings.TrimPrefix(name, "/") + "</command-message>\n            <command-args>" + args + "</command-args>"
	}
	out := func(text string) string { return "<local-command-stdout>" + text + "</local-command-stdout>" }
	for _, format := range []struct {
		name string
		line func(ts, content string) string
	}{{"type user (2.1.309)", user}, {"system local_command (older)", system}} {
		t.Run(format.name, func(t *testing.T) {
			ts := func(n int) string { return time.Date(2026, 10, 23, 0, 0, n, 0, time.UTC).Format(time.RFC3339Nano) }
			lines := []string{
				format.line(ts(1), cmd("/model", "claude-sonnet-5-5")),
				format.line(ts(1), out("Set model to Sonnet 5.5")),
				format.line(ts(2), cmd("/model", "nope")),
				format.line(ts(2), out("Unknown model: nope")),
				format.line(ts(3), cmd("/effort", "max")),
				format.line(ts(3), out("Set effort level to max (this session only): Maximum capability")),
				format.line(ts(4), cmd("/effort", "bogus")),
				format.line(ts(4), out("Invalid effort level: bogus")),
			}
			got := parseLocalCommands([]byte(strings.Join(lines, "\n")), time.Time{})
			require.Len(t, got, 4)
			assert.Equal(t, "/model", got[0].Name)
			assert.Equal(t, "claude-sonnet-5-5", got[0].Args)
			assert.Equal(t, "Set model to Sonnet 5.5", got[0].Output)
			assert.Equal(t, "Unknown model: nope", got[1].Output)
			assert.Equal(t, "/effort", got[2].Name)
			assert.True(t, strings.HasPrefix(got[2].Output, "Set effort level to max"))
			assert.Equal(t, "Invalid effort level: bogus", got[3].Output)
		})
	}
	t.Run("quotes of the tags are not commands", func(t *testing.T) {
		quoted := user("2026-10-23T00:00:01Z", "please run <command-name>/model</command-name> for me") // a prompt that goes on after the tag
		toolResult, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-10-23T00:00:02Z", "message": map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": cmd("/model", "x")}}}})
		assert.Empty(t, parseLocalCommands([]byte(quoted+"\n"+string(toolResult)), time.Time{}), "a prompt that quotes the tag, and a tool result that holds it")
	})
}
