package jsonl

import (
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
