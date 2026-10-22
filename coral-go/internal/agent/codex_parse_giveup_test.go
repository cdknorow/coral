package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Codex rollout that cannot be parsed was opened again on every indexer pass, for ever (#6098). After three passes in a row it is left alone until its mtime changes.
func TestCodexRolloutThatFailsToParseIsGivenUpAfterThreePasses(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "rollout-bad.jsonl")
	good := filepath.Join(dir, "rollout-good.jsonl")
	data, err := os.ReadFile(filepath.Join("testdata", "codex-0163-parent.jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(bad, []byte("not a rollout"), 0o600))
	require.NoError(t, os.WriteFile(good, data, 0o600))

	opens := map[string]int{}
	a := &CodexAgent{openHistory: func(p string) (*os.File, error) {
		opens[filepath.Base(p)]++
		if p == bad {
			return nil, errors.New("cannot be read") // a permission, or a rollout being written
		}
		return openHistoryFile(p)
	}}
	known := map[string]float64{}
	pass := func() {
		got, err := a.ExtractSessions(dir, known)
		require.NoError(t, err)
		for _, s := range got {
			known[s.SourceFile] = s.FileMtime // what the indexer stores for the file it indexed
		}
	}

	for i := 1; i <= 3; i++ {
		pass()
		assert.Equal(t, i, opens["rollout-bad.jsonl"], "pass %d still tries it", i)
	}
	pass()
	pass()
	assert.Equal(t, 3, opens["rollout-bad.jsonl"], "given up: later passes do not open it")
	assert.Equal(t, 1, opens["rollout-good.jsonl"], "a file that parsed is skipped by its stored mtime, as before")

	// it changed (Codex finished writing it): tried again, and given up again only after three more failures
	info, err := os.Stat(bad)
	require.NoError(t, err)
	stamp := info.ModTime().Add(10 * time.Second)
	require.NoError(t, os.Chtimes(bad, stamp, stamp))
	pass()
	assert.Equal(t, 4, opens["rollout-bad.jsonl"], "a changed file is checked again")
	pass()
	pass()
	pass()
	assert.Equal(t, 6, opens["rollout-bad.jsonl"], "three failures at the new mtime, then given up again")
}

func TestCodexParseFailureMarksGoWithTheirFiles(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "rollout-gone.jsonl")
	require.NoError(t, os.WriteFile(bad, []byte("x"), 0o600))
	a := &CodexAgent{openHistory: func(string) (*os.File, error) { return nil, errors.New("no") }}
	_, err := a.ExtractSessions(dir, nil)
	require.NoError(t, err)
	a.mu.Lock()
	require.Len(t, a.failures, 1)
	a.mu.Unlock()
	require.NoError(t, os.Remove(bad))
	_, err = a.ExtractSessions(dir, nil)
	require.NoError(t, err)
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Empty(t, a.failures, "the map does not outgrow the rollouts that exist")
}
