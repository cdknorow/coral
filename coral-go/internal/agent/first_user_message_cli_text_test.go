package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6207 review: text the CLI wrote is not what the person typed. A session whose first user entries are a compaction summary, an injected (isMeta) entry, a subagent's prompt, an interrupt marker or a
// task notification keeps the first thing actually typed after them, and a Codex session with only an AGENTS.md item has none rather than that block.
func TestFirstUserMessageSkipsWhatTheCLIWrote(t *testing.T) {
	base := t.TempDir()
	writeLines(t, filepath.Join(base, "p", "00000000-0000-4000-8000-000000006299.jsonl"),
		`{"type":"user","isCompactSummary":true,"timestamp":"2026-10-23T00:00:00Z","message":{"role":"user","content":"This session is being continued from a previous conversation that ran out of context."}}`,
		`{"type":"user","isMeta":true,"timestamp":"2026-10-23T00:00:01Z","message":{"role":"user","content":"Base directory for this skill: /x"}}`,
		`{"type":"user","isSidechain":true,"timestamp":"2026-10-23T00:00:02Z","message":{"role":"user","content":"subagent prompt"}}`,
		`{"type":"user","timestamp":"2026-10-23T00:00:03Z","message":{"role":"user","content":"[Request interrupted by user]"}}`,
		`{"type":"user","timestamp":"2026-10-23T00:00:04Z","message":{"role":"user","content":"<task-notification><task-id>1</task-id></task-notification>"}}`,
		`{"type":"user","timestamp":"2026-10-23T00:00:05Z","message":{"role":"user","content":"what I typed"}}`,
		`{"type":"assistant","timestamp":"2026-10-23T00:00:06Z","message":{"content":[{"type":"text","text":"ok"}]}}`)
	got, err := (&ClaudeAgent{}).ExtractSessions(base, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "what I typed", got[0].FirstUserMessage)

	p := filepath.Join(t.TempDir(), "rollout-2026-10-23T00-00-09-019e90eb-a08a-7511-a410-000000006297.jsonl")
	writeLines(t, p,
		`{"timestamp":"2026-10-23T00:00:00.100Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /x\n\n<INSTRUCTIONS>be nice</INSTRUCTIONS>"}]}}`,
		`{"timestamp":"2026-10-23T00:00:02.000Z","type":"event_msg","payload":{"type":"agent_message","message":"hi","phase":"final_answer"}}`)
	s, err := parseCodexSession(p, 1)
	require.NoError(t, err)
	assert.False(t, strings.Contains(s.FirstUserMessage, "AGENTS.md"), "%q", s.FirstUserMessage)
}
