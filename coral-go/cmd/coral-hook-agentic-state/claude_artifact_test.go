package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Artifact tool call's event keeps what Coral needs to tell a publish from a read and one artifact from another (#6583), and nothing of the page.
func TestArtifactToolEventKeepsActionFileAndURLOnly(t *testing.T) {
	e := parseAgenticEvent(map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Artifact", "tool_use_id": "tu1",
		"tool_input": map[string]any{"action": "publish", "file_path": "/w/page.html", "url": "https://claude.ai/artifact/abcdefgh1", "description": "a secret", "capabilities": map[string]any{"x": 1}},
	}, "PostToolUse", "sid")
	require.NotNil(t, e)
	assert.Equal(t, "Artifact", e["tool_name"])
	d, _ := e["detail_json"].(map[string]any)
	assert.Equal(t, "publish", d["action"])
	assert.Equal(t, "/w/page.html", d["file_path"])
	assert.Equal(t, "https://claude.ai/artifact/abcdefgh1", d["url"])
	assert.NotContains(t, d, "description")
	assert.NotContains(t, d, "capabilities")

	e = parseAgenticEvent(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Artifact", "tool_input": map[string]any{"action": "read", "url": "https://evil.example/x"}}, "PostToolUse", "sid")
	d, _ = e["detail_json"].(map[string]any)
	assert.Equal(t, "read", d["action"])
	assert.NotContains(t, d, "url", "only a claude.ai url is kept")
}

// A Claude agent's Stop event carries the claude.ai artifact ids in the reply it ended with, read from its own transcript.
func TestStopEventCarriesArtifactIDsFromTheReply(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done: https://claude.ai/artifact/abcdefgh1"}]}}`+"\n"), 0o600))
	t.Setenv("CORAL_SESSION_NAME", "claude-1234")
	e := parseAgenticEvent(map[string]any{"hook_event_name": "Stop", "transcript_path": p}, "Stop", "sid")
	d, _ := e["detail_json"].(map[string]any)
	assert.Equal(t, []string{"abcdefgh1"}, d["claude_artifact_ids"])

	t.Setenv("CORAL_SESSION_NAME", "codex-1234")
	e = parseAgenticEvent(map[string]any{"hook_event_name": "Stop", "transcript_path": p}, "Stop", "sid")
	assert.NotContains(t, e, "detail_json", "only a Claude agent's transcript is read")
}
