package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4590: a Codex started from another Codex session's shell inherits that session's
// CODEX_THREAD_ID. The thread a hook binds is the one in Codex's own payload, never the
// environment's.
func TestCodexThreadID_IsThePayloadsNotTheEnvironments(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "0aaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa") // the parent's, inherited
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-0.155.1", "SessionStart.json"))
	require.NoError(t, err)
	var d map[string]any
	require.NoError(t, json.Unmarshal(raw, &d))
	assert.Equal(t, "01a14d4e-1a66-7893-b33a-f0de799b1d64", codexThreadID(d), "the payload's own session_id")

	assert.Empty(t, codexThreadID(map[string]any{}), "no payload id: no binding; the environment is not a fallback")
}
