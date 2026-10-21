package jsonl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5820 (QA's measurement on #5811: 3.5 s with 3000 Codex rollouts and about 4 minutes with 10000, for a bookmark of a session with no transcript here).

func writeRollouts(t *testing.T, home string, n int) {
	t.Helper()
	line := `{"type":"event_msg","payload":{"type":"agent_message","message":"` + strings.Repeat("x", 80) + `"}}` + "\n"
	body := []byte(strings.Repeat(line, 80)) // about 8 KB, no Coral marker: the scan reads all of it
	for i := 0; i < n; i++ {
		dir := filepath.Join(home, "sessions", "2026", fmt.Sprintf("%02d", 1+i%12), fmt.Sprintf("%02d", 1+i%28))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("rollout-2026-10-21T10-00-00-%08d-0000-4000-8000-000000000000.jsonl", i)), body, 0o644))
	}
}

func resetCodexMarkers() {
	codexMarkers.Lock()
	codexMarkers.entries, codexMarkers.order = nil, nil
	codexMarkers.Unlock()
}

func codexMarkerCount() int {
	codexMarkers.Lock()
	defer codexMarkers.Unlock()
	return len(codexMarkers.entries)
}

// A session of another agent type never walks ~/.codex: only the type asked is looked in.
func TestFingerprintOfAClaudeSessionNeverWalksTheCodexTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	writeRollouts(t, home, 300)
	resetCodexMarkers()
	_, _, ok, aborted := TranscriptFingerprintCtx(context.Background(), "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee1", "", "claude")
	assert.False(t, ok)
	assert.False(t, aborted)
	assert.Zero(t, codexMarkerCount(), "no rollout was opened for a Claude session")
	// the same id asked as a Codex session does walk it (and finds nothing)
	_, _, ok, _ = TranscriptFingerprintCtx(context.Background(), "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee1", "", "codex")
	assert.False(t, ok)
	assert.NotZero(t, codexMarkerCount())
}

// A walk of thousands of rollouts stops when its context ends, and says it was cut off (which is not "no transcript").
func TestCodexWalkStopsWhenItsContextEnds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeRollouts(t, home, 5000)
	resetCodexMarkers()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, ok, aborted := TranscriptFingerprintCtx(ctx, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee1", "", "codex")
	took := time.Since(start)
	assert.False(t, ok)
	assert.True(t, aborted, "cut off, not 'no transcript'")
	assert.Less(t, took, 2*time.Second, "the walk of 5000 rollouts ended with its context")
}

// The marker cache drops the least recently used rollout when it is full, not everything at once.
func TestCodexMarkerCacheIsAnLRUNotClearedWhole(t *testing.T) {
	resetCodexMarkers()
	put := func(i int) { codexMarkerPut(fmt.Sprintf("/r/%d", i), codexMarkerEntry{id: fmt.Sprint(i)}) }
	for i := 0; i < codexMarkerMax; i++ {
		put(i)
	}
	assert.Equal(t, codexMarkerMax, codexMarkerCount())
	_, ok := codexMarkerGet("/r/0") // used: now the newest
	require.True(t, ok)
	put(codexMarkerMax) // one over: the oldest unused (1) goes, one entry only
	assert.Equal(t, codexMarkerMax, codexMarkerCount(), "one entry left, not all of them")
	_, ok = codexMarkerGet("/r/1")
	assert.False(t, ok, "the least recently used is the one dropped")
	_, ok = codexMarkerGet("/r/0")
	assert.True(t, ok, "a recently used one stays")
	// more rollouts than the old limit of 4096 are all remembered
	assert.Greater(t, codexMarkerMax, 4096)
}

// The list's lookup of a Codex rollout is by its link and its file name only: a rollout that holds the session id in its name is found; one that does not is "not told" (the scan
// of every rollout for the session's marker is left to the one who opens the bookmark), and the scan is not run.
func TestListFingerprintOfACodexRolloutIsByFileNameOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeRollouts(t, home, 200)
	const named = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee1"
	dir := filepath.Join(home, "sessions", "2026", "10", "21")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-2026-10-21T10-00-00-"+named+".jsonl"), []byte(`{"type":"event_msg"}`+"\n"), 0o644))
	resetCodexMarkers()
	path, _, ok, aborted, unsure := TranscriptFingerprintListCtx(context.Background(), named, "", "codex")
	assert.True(t, ok)
	assert.False(t, aborted || unsure)
	assert.Contains(t, path, named)
	_, _, ok, aborted, unsure = TranscriptFingerprintListCtx(context.Background(), "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee2", "", "codex")
	assert.False(t, ok)
	assert.False(t, aborted)
	assert.True(t, unsure, "not found by name: not told, not 'no transcript'")
	assert.Zero(t, codexMarkerCount(), "no rollout was opened to look for a marker")
	// another agent type is never unsure: its lookup is a stat
	_, _, ok, _, unsure = TranscriptFingerprintListCtx(context.Background(), "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee2", "", "claude")
	assert.False(t, ok || unsure)
}
