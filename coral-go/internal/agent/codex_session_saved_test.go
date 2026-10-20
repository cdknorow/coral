package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5222: Codex writes a rollout file only after a turn; "codex resume" of a session without one ends at "No saved session found".
func TestCodexSessionSaved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	const coral = "11111111-2222-3333-4444-555555555555"
	const thread = "019e8bbc-7271-7e13-bd16-e2ead918e81a"
	rollout := func(id string) {
		dir := filepath.Join(home, "sessions", "2026", "10", "19")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-2026-10-19T10-00-00-"+id+".jsonl"), []byte(`{"type":"session_meta"}`+"\n"), 0o600))
	}
	link := func(coralID, threadID string) {
		dir := filepath.Join(home, "coral-session-links")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, coralID+".json"), []byte(`{"thread_id":"`+threadID+`"}`), 0o600))
	}

	assert.False(t, CodexSessionSaved(coral), "nothing saved: no rollout")
	link(coral, thread)
	assert.False(t, CodexSessionSaved(coral), "a link to a thread that has no rollout")
	rollout(thread)
	assert.True(t, CodexSessionSaved(coral), "the link's thread has its rollout")
	// With no link the id itself is the thread.
	const bare = "22222222-2222-3333-4444-555555555555"
	assert.False(t, CodexSessionSaved(bare))
	rollout(bare)
	assert.True(t, CodexSessionSaved(bare))
	// Not an id: never a glob.
	for _, id := range []string{"", "*", "../x", "[0-9]*", coral + "*"} {
		assert.False(t, CodexSessionSaved(id), "%q", id)
	}
}
