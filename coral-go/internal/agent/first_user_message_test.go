package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6207: History titles lead with what the person typed first, not the assistant's first reply. Each of the four indexers keeps it, never a slash command's tags or text the CLI injected, as a short single
// line, and stamps the parser version so rows indexed before are read again once.

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

func TestClaudeIndexerKeepsTheFirstUserMessage(t *testing.T) {
	base := t.TempDir()
	long := strings.Repeat("word ", 100)
	writeLines(t, filepath.Join(base, "proj", "00000000-0000-4000-8000-000000006207.jsonl"),
		`{"type":"user","timestamp":"2026-10-23T00:00:00Z","message":{"role":"user","content":"<command-name>/model</command-name>\n <command-message>model</command-message>\n <command-args>x</command-args>"}}`,
		`{"type":"user","timestamp":"2026-10-23T00:00:01Z","message":{"role":"user","content":"<local-command-stdout>Set model to X</local-command-stdout>"}}`,
		`{"type":"user","timestamp":"2026-10-23T00:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}}`,
		`{"type":"user","timestamp":"2026-10-23T00:00:03Z","message":{"role":"user","content":"  Which   folder\nare you in? `+long+`"}}`,
		`{"type":"assistant","timestamp":"2026-10-23T00:00:04Z","message":{"content":[{"type":"text","text":"I am in the project folder."}]}}`,
		`{"type":"user","timestamp":"2026-10-23T00:00:05Z","message":{"role":"user","content":"a second question"}}`)
	got, err := (&ClaudeAgent{}).ExtractSessions(base, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, strings.HasPrefix(got[0].FirstUserMessage, "Which folder are you in? word word"), "%q", got[0].FirstUserMessage)
	assert.LessOrEqual(t, len(got[0].FirstUserMessage), firstMessageLimit)
	assert.NotContains(t, got[0].FirstUserMessage, "\n", "one line")
	assert.Equal(t, "I am in the project folder.", got[0].DisplaySummary, "the summary is still the assistant's first reply")
	assert.Equal(t, HistoryIndexVersion, got[0].IndexVersion)
}

func TestCodexIndexerKeepsTheFirstUserMessage(t *testing.T) {
	dir := t.TempDir()
	t.Run("the typed message wins over an injected item", func(t *testing.T) {
		p := filepath.Join(dir, "rollout-2026-10-23T00-00-00-019e90eb-a08a-7511-a410-000000006207.jsonl")
		writeLines(t, p,
			`{"timestamp":"2026-10-23T00:00:00.000Z","type":"session_meta","payload":{"id":"019e90eb-a08a-7511-a410-000000006207"}}`,
			`{"timestamp":"2026-10-23T00:00:00.100Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>cwd /x</environment_context>"}]}}`,
			`{"timestamp":"2026-10-23T00:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"which folder are you in?","images":[]}}`,
			`{"timestamp":"2026-10-23T00:00:02.000Z","type":"event_msg","payload":{"type":"agent_message","message":"I am in the project folder.","phase":"final_answer"}}`)
		s, err := parseCodexSession(p, 1)
		require.NoError(t, err)
		require.NotNil(t, s)
		assert.Equal(t, "which folder are you in?", s.FirstUserMessage)
		assert.Equal(t, HistoryIndexVersion, s.IndexVersion)
	})
	t.Run("a user message item is the fallback", func(t *testing.T) {
		p := filepath.Join(dir, "rollout-2026-10-23T00-00-01-019e90eb-a08a-7511-a410-000000006208.jsonl")
		writeLines(t, p,
			`{"timestamp":"2026-10-23T00:00:00.100Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<user_instructions>x</user_instructions>"}]}}`,
			`{"timestamp":"2026-10-23T00:00:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"summarise the repo"}]}}`,
			`{"timestamp":"2026-10-23T00:00:02.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`)
		s, err := parseCodexSession(p, 1)
		require.NoError(t, err)
		require.NotNil(t, s)
		assert.Equal(t, "summarise the repo", s.FirstUserMessage)
	})
}

func TestAgyAndGeminiIndexersKeepTheFirstUserMessage(t *testing.T) {
	dir := t.TempDir()
	agy := filepath.Join(dir, "conv.jsonl")
	writeLines(t, agy,
		`{"type":"USER_INPUT","content":"how do I list files?","created_at":"2026-10-23T00:00:00Z"}`,
		`{"type":"PLANNER_RESPONSE","content":"Use ls.","created_at":"2026-10-23T00:00:01Z"}`,
		`{"type":"USER_INPUT","content":"and hidden ones?","created_at":"2026-10-23T00:00:02Z"}`)
	s, err := parseAgyTranscriptSession("conv", agy, 1)
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Equal(t, "how do I list files?", s.FirstUserMessage)
	assert.Equal(t, "how do I list files?", s.DisplaySummary, "Antigravity's summary is the first content, as before")
	assert.Equal(t, HistoryIndexVersion, s.IndexVersion)

	gem := filepath.Join(dir, "session-abc.json")
	require.NoError(t, os.WriteFile(gem, []byte(`[{"role":"user","timestamp":"2026-10-23T00:00:00Z","parts":[{"text":"what is Go?"}]},{"role":"model","timestamp":"2026-10-23T00:00:01Z","parts":[{"text":"A language."}]}]`), 0o644))
	g, err := parseGeminiSession(gem, 1)
	require.NoError(t, err)
	require.NotNil(t, g)
	assert.Equal(t, "what is Go?", g.FirstUserMessage)
	assert.Equal(t, "A language.", g.DisplaySummary)
	assert.Equal(t, HistoryIndexVersion, g.IndexVersion)
}

func TestPiIndexerKeepsTheFirstUserMessage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pi.jsonl")
	writeLines(t, p,
		`{"type":"session","id":"pi-6207"}`,
		`{"type":"message","role":"assistant","content":"hello","timestamp":"2026-10-23T00:00:00Z"}`,
		`{"type":"message","role":"user","content":"what can you do?","timestamp":"2026-10-23T00:00:01Z"}`)
	s, err := parsePiSession(p, 1)
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Equal(t, "what can you do?", s.FirstUserMessage)
	assert.Equal(t, HistoryIndexVersion, s.IndexVersion)
}

func TestClipFirstMessageCutsOnACharacterBoundary(t *testing.T) {
	got := clipFirstMessage(strings.Repeat("é", 150)) // 300 bytes
	assert.LessOrEqual(t, len(got), firstMessageLimit)
	assert.True(t, strings.HasPrefix(strings.Repeat("é", 150), got), "no half character: %q", got[len(got)-2:])
}
