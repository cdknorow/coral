package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/transcriptlink"
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

// #6325: Codex's hooks were not trusted, so none wrote the canonical-to-native link. The wake still finds the conversation by the Coral marker in its rollout, binds it, and resumes the native thread;
// a rollout with no marker, or another session's, is not taken (the agent then starts fresh, as before).
func TestCodexSessionSavedFindsTheThreadByItsMarkerWithNoLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	const coral = "5f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	const other = "6f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	const thread = "019e8bbc-7271-7e13-bd16-e2ead918e81b"
	rollout := func(id, marker string) {
		day := time.Now()
		dir := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
		require.NoError(t, os.MkdirAll(dir, 0o700))
		body := `{"type":"session_meta","payload":{"id":"` + id + `"}}` + "\n" +
			`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\nCORAL_SESSION_ID: ` + marker + `"}]}}` + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-"+day.Format("2006-01-02")+"T09-00-00-"+id+".jsonl"), []byte(body), 0o600))
	}

	rollout(thread, other)
	assert.False(t, CodexSessionSaved(coral), "the only rollout names another session")
	assert.Empty(t, transcriptlink.Codex(coral).ThreadID)

	rollout(thread, coral) // the same thread, now marked as this session's
	require.True(t, CodexSessionSaved(coral), "no link, but the rollout carries this session's marker")
	assert.Equal(t, thread, transcriptlink.Codex(coral).ThreadID, "the thread is bound for the wake")
	parts, _ := codexStart(LaunchParams{ResumeSessionID: coral})
	assert.Equal(t, []string{"resume", thread}, parts[len(parts)-2:], "the wake resumes the native thread, not the Coral id")
}
