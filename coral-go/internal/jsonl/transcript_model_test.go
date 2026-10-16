package jsonl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	for _, l := range lines {
		_, err = f.WriteString(l + "\n")
		require.NoError(t, err)
	}
	require.NoError(t, f.Close())
}

// #4065: the model a Claude agent actually uses is its newest assistant entry's message.model
// (it follows a /model switch); Claude Code's own "<synthetic>" entries and other lines don't
// change it. TranscriptModel only reports what a turn read already found.
func TestClaudeTranscriptModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000004065"
	path := filepath.Join(dir, "x", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	appendLines(t, path,
		`{"sessionId":"`+sid+`","type":"user","message":{"role":"user","content":"hi, use model claude-fake"},"timestamp":"2026-10-16T00:00:00Z"}`,
		`{"sessionId":"`+sid+`","type":"assistant","message":{"model":"claude-opus-5-5","role":"assistant","content":[]},"timestamp":"2026-10-16T00:00:01Z"}`,
	)
	r := NewSessionReader()
	assert.Equal(t, "", r.TranscriptModel(sid), "nothing read yet: never reads the file itself")
	r.ReadClaudeTurnEvent(sid, "")
	assert.Equal(t, "claude-opus-5-5", r.TranscriptModel(sid))

	appendLines(t, path,
		`{"sessionId":"`+sid+`","type":"assistant","message":{"model":"<synthetic>","role":"assistant","content":[]},"timestamp":"2026-10-16T00:00:02Z"}`,
		`{"sessionId":"`+sid+`","type":"user","message":{"role":"user","content":"\"model\":\"evil\""},"timestamp":"2026-10-16T00:00:03Z"}`,
	)
	r.ReadClaudeTurnEvent(sid, "")
	assert.Equal(t, "claude-opus-5-5", r.TranscriptModel(sid), "synthetic and user lines don't count")

	appendLines(t, path, `{"sessionId":"`+sid+`","type":"assistant","message":{"model":"claude-sonnet-5-5","role":"assistant","content":[]},"timestamp":"2026-10-16T00:01:00Z"}`)
	r.ReadClaudeTurnEvent(sid, "")
	assert.Equal(t, "claude-sonnet-5-5", r.TranscriptModel(sid), "after /model")
}

// #4065: Codex records the model on each turn's turn_context.
func TestCodexTranscriptModel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	appendLines(t, p,
		`{"timestamp":"2026-10-16T00:00:00.000Z","type":"session_meta","payload":{"id":"x","model_provider":"openai"}}`,
		`{"timestamp":"2026-10-16T00:00:01.000Z","type":"turn_context","payload":{"cwd":"/w","model":"gpt-6-astra","effort":"medium"}}`,
		`{"timestamp":"2026-10-16T00:00:02.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
	)
	var state codexTurnCache
	readCodexTurnEventIncremental(p, &state)
	assert.Equal(t, "gpt-6-astra", state.model)
	appendLines(t, p, `{"timestamp":"2026-10-16T00:01:00.000Z","type":"turn_context","payload":{"model":"gpt-6-sol"}}`)
	readCodexTurnEventIncremental(p, &state)
	assert.Equal(t, "gpt-6-sol", state.model, "the newest turn's")

	r := NewSessionReader()
	r.getOrCreateSessionCache("codex-sid").codexState = state
	assert.Equal(t, "gpt-6-sol", r.TranscriptModel("codex-sid"))
}

// A model id is kept only when it looks like one.
func TestCleanModelID(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5-5": "claude-opus-5-5", " gpt-6-astra ": "gpt-6-astra", "opus[1m]": "opus[1m]",
		"<synthetic>": "", "": "", "-x": "", "a b": "", "a\x1bb": "",
	} {
		assert.Equal(t, want, cleanModelID(in), "%q", in)
	}
}
