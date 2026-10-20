package jsonl

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
