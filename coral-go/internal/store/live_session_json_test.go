package store

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6146: a live session's JSON says launch_path, not backend: the stored label names the launch path ("pty" means through the terminal backend, a tmux Coral's included), and "backend" read as the terminal the
// agent runs on. Pinned here, with no tmux needed, so it holds on every machine.
func TestALiveSessionsJSONSaysLaunchPath(t *testing.T) {
	label := "pty"
	b, err := json.Marshal(LiveSession{SessionID: "s", AgentType: "claude", Backend: &label})
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	assert.Equal(t, "pty", m["launch_path"])
	assert.NotContains(t, m, "backend")

	b, err = json.Marshal(LiveSession{SessionID: "s", AgentType: "claude"})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "launch_path", "no label: the field is left out, as before")
}
