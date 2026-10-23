package jsonl

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6658: Codex writes bookkeeping records that carry no turn content (token_usage_record, turn_context). One written after a permission request opened must not be taken for the turn going on:
// the turn's activity time is what closes an open request, so a late bookkeeping line would release a held chat message into the permission box. The sequence is Codex Test's real one (#6646):
// the call recorded just before its dialog, then the bookkeeping, with the dialog open.
func TestCodexBookkeepingRecordsAreNotTurnActivity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	lines := `{"timestamp":"2026-10-23T19:17:00.000Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-10-23T19:17:03.000Z","type":"response_item","payload":{"type":"custom_tool_call"}}` + "\n" +
		`{"timestamp":"2026-10-23T19:17:05.100Z","type":"token_usage_record"}` + "\n" + // 2 s after the dialog opened
		`{"timestamp":"2026-10-23T19:17:06.200Z","type":"turn_context","payload":{"model":"gpt-test","effort":"high"}}` + "\n" +
		`{"timestamp":"2026-10-23T19:17:07.300Z","type":"event_msg","payload":{"type":"item_completed"}}` + "\n" // what a tool running in parallel writes while a dialog waits (Codex Test's #6657)
	require.NoError(t, os.WriteFile(p, []byte(lines), 0o600))
	var state codexTurnCache
	kind, at := readCodexTurnEventIncremental(p, &state)
	assert.Equal(t, "prompt_submit", kind)
	assert.Equal(t, "2026-10-23T19:17:03Z", at.Format(time.RFC3339), "the turn's last activity is the call: the bookkeeping and the parallel tool's item_completed after it did not move it")
	assert.Equal(t, "gpt-test", state.model, "the turn_context is still read for its model")
	assert.Equal(t, "high", state.effort)

	// A record of the turn after them does move it.
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"timestamp":"2026-10-23T19:17:21.074Z","type":"response_item","payload":{"type":"custom_tool_call_output"}}` + "\n")
	require.NoError(t, err)
	f.Close()
	kind, at = readCodexTurnEventIncremental(p, &state)
	assert.Equal(t, "prompt_submit", kind)
	assert.Equal(t, "2026-10-23T19:17:21Z", at.Format(time.RFC3339))
}

// Bookkeeping outside a turn changes nothing, and a turn's start and end are still read.
func TestCodexBookkeepingDoesNotMoveAnIdleOrEndedTurn(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	lines := `{"timestamp":"2026-10-23T19:17:00.000Z","type":"turn_context","payload":{"model":"gpt-test"}}` + "\n" +
		`{"timestamp":"2026-10-23T19:17:01.000Z","type":"token_usage_record"}` + "\n"
	require.NoError(t, os.WriteFile(p, []byte(lines), 0o600))
	var state codexTurnCache
	kind, _ := readCodexTurnEventIncremental(p, &state)
	assert.Empty(t, kind, "no turn yet: nothing")
	lines += `{"timestamp":"2026-10-23T19:17:02.000Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" +
		`{"timestamp":"2026-10-23T19:17:09.000Z","type":"event_msg","payload":{"type":"turn_aborted"}}` + "\n" +
		`{"timestamp":"2026-10-23T19:17:12.000Z","type":"token_usage_record"}` + "\n"
	require.NoError(t, os.WriteFile(p, []byte(lines), 0o600))
	state = codexTurnCache{}
	kind, at := readCodexTurnEventIncremental(p, &state)
	assert.Equal(t, "session_reset", kind, "the abort stands; a bookkeeping line after it is not a turn")
	assert.Equal(t, "2026-10-23T19:17:09Z", at.Format(time.RFC3339))
}
