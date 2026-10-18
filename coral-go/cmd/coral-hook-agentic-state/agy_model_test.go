package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4588: agy's payloads name the model (modelName); the hook reports it on the two events that
// open a session or a turn, from the recorded agy 1.3.5 payloads.
func TestAgyModelOf_FromRecordedPayloads(t *testing.T) {
	load := func(name string) map[string]any {
		raw, err := os.ReadFile(filepath.Join("testdata", "agy-1.3.5", name))
		require.NoError(t, err)
		var d map[string]any
		require.NoError(t, json.Unmarshal(raw, &d))
		return d
	}
	assert.Equal(t, "gemini-3.8-flash-high", agyModelOf(load("SessionStart.json"), "SessionStart"))
	assert.Equal(t, "gemini-3.8-flash-high", agyModelOf(load("PreInvocation-0.json"), "PreInvocation"))
	assert.Empty(t, agyModelOf(load("PreInvocation-1.json"), "PreInvocation"), "a later invocation of the turn")
	for ev, f := range map[string]string{"PostToolUse": "PostToolUse.json", "PreToolUse": "PreToolUse.json", "Stop": "Stop.json", "PostInvocation": "PostInvocation.json"} {
		assert.Empty(t, agyModelOf(load(f), ev), ev)
	}
	assert.Empty(t, agyModelOf(map[string]any{"model": "claude-opus-5-5"}, ""), "not an agy hook")
	assert.Empty(t, agyModelOf(map[string]any{}, "SessionStart"))
}
