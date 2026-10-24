package routes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CodeQL #399-407, #435-436 (#7046): persistSessionChanges names a folder from the session id, so it checks the id as the read paths do: an id that is not one path element writes nothing, outside the sessions folder least of all.
func TestPersistSessionChangesRefusesAnUnsafeSessionID(t *testing.T) {
	_, h, _, _ := setupSessionsTestServer(t)
	repo, git := repoOn(t, "master", true)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("a change\n"), 0o644))
	git("add", "changed.txt")
	artifacts := filepath.Join(h.cfg.CoralDir(), "artifacts")
	for _, id := range []string{"../escape", "..", ".", "a/b", `a\b`, "x/../../y"} {
		path, err := h.persistSessionChanges(t.Context(), id, repo)
		assert.Error(t, err, "%q", id)
		assert.Empty(t, path, "%q", id)
	}
	for _, p := range []string{"escape", "y"} {
		assert.NoFileExists(t, filepath.Join(artifacts, p, "changes.diff"))
	}
	entries, _ := os.ReadDir(filepath.Join(artifacts, "sessions"))
	assert.Empty(t, entries, "nothing was written for any of them")

	path, err := h.persistSessionChanges(t.Context(), "11111111-2222-3333-4444-555555555555", repo)
	require.NoError(t, err, "a real session id still works")
	assert.FileExists(t, path)
}
