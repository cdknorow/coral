package jsonl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A slash command Claude Code ran by itself is a turn in the chat, as typed (#5488), so the message the person sent is not left pending; its output entry is not a turn.
func TestParseClaudeLocalCommandIsATurnAsTyped(t *testing.T) {
	cmd := map[string]any{"type": "system", "subtype": "local_command", "timestamp": "2026-10-20T10:00:00Z",
		"content": "<command-name>/model</command-name>\n            <command-message>model</command-message>\n            <command-args>claude-sonnet-4-5</command-args>"}
	got := parseClaudeEntry(cmd, nil)
	assert.Equal(t, []map[string]any{{"type": "user", "timestamp": "2026-10-20T10:00:00Z", "content": "/model claude-sonnet-4-5", "local_command": true}}, got)

	bare := map[string]any{"type": "system", "subtype": "local_command", "timestamp": "t", "content": "<command-name>/effort</command-name><command-args></command-args>"}
	assert.Equal(t, "/effort", parseClaudeEntry(bare, nil)[0]["content"], "no arguments: the name alone")

	out := map[string]any{"type": "system", "subtype": "local_command", "timestamp": "t", "content": "<local-command-stdout>Set model to Sonnet 4.5</local-command-stdout>"}
	assert.Nil(t, parseClaudeEntry(out, nil), "what the command printed is not a turn")
	other := map[string]any{"type": "system", "subtype": "informational", "content": "<command-name>/model</command-name>"}
	assert.Nil(t, parseClaudeEntry(other, nil), "another kind of system entry is not")
	// text that only looks like a command is never taken: the escape characters in an argument are cleaned
	odd := map[string]any{"type": "system", "subtype": "local_command", "timestamp": "t", "content": "<command-name>/model</command-name><command-args>x\x1b[31m red\x07</command-args>"}
	assert.Equal(t, "/model x[31m red", parseClaudeEntry(odd, nil)[0]["content"])
}

// Claude Code 2.1.309 records a slash command and its output as type "user" entries; the chat showed "/effort effort low" and a bubble of the output (#6219, finding 4). Through the real records: the command is
// one compact note as typed, the output is no turn, and a prompt that merely quotes the tag is still a message.
func TestParseClaudeLocalCommandInTheUserFormat(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "claude_2_1_309_effort_commands.jsonl"))
	require.NoError(t, err)
	var notes []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		for _, turn := range parseClaudeEntry(entry, nil) {
			assert.Equal(t, true, turn["local_command"], "only the command is a turn, flagged as a note")
			notes = append(notes, turn["content"].(string))
		}
	}
	assert.Equal(t, []string{"/effort low", "/effort high", "/effort xhigh", "/effort max", "/effort auto"}, notes, "one compact line per command; its output is not a bubble")

	quoted := map[string]any{"type": "user", "timestamp": "t", "message": map[string]any{"role": "user", "content": "please run <command-name>/model</command-name> for me"}}
	got := parseClaudeEntry(quoted, nil)
	require.Len(t, got, 1)
	assert.Nil(t, got[0]["local_command"], "a message that quotes the tag stays a message")
}
