package agentsignin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Snapshot (the /api/system/status handler) must not wait for a check that is in flight.
func TestProbeSnapshotDoesNotWaitForARunningCheck(t *testing.T) {
	reset(t)
	checkTimeout = 1500 * time.Millisecond
	bin := standInBin(t, "hang")
	done := make(chan struct{})
	go func() { _ = RequireFresh(context.Background(), "claude", bin); close(done) }()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	Snapshot("claude")
	took := time.Since(start)
	<-done
	assert.Less(t, took, 300*time.Millisecond, "Snapshot waited for the running check")
}

// A settings.json whose env has one non-string value hides an apiKeyHelper.
func TestProbeSettingsWithANonStringEnvValueStillShowsTheKeyHelper(t *testing.T) {
	reset(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"apiKeyHelper":"/x/get-key","env":{"MAX_THINKING_TOKENS":1024}}`), 0o600))
	assert.True(t, credentialedOutsideLogin())
}

// #5476 (the Orchestrator's question): the CLI reads the project settings of its working folder. A user whose only credential is in a repository's .claude/settings.json is signed in
// for jobs that run in that folder, and the check must ask from there, not from the temp folder.
func TestTheCheckAsksFromTheFolderTheJobRunsIn(t *testing.T) {
	reset(t)
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(`{"apiKeyHelper":"/x/get-key"}`), 0o600))
	other := t.TempDir()
	bin := standInBin(t, "claude-by-cwd")

	assert.Equal(t, SignedIn, CheckIn(context.Background(), "claude", bin, repo), "the folder with the credential")
	assert.Equal(t, SignedOut, CheckIn(context.Background(), "claude", bin, other), "a folder without one")
	assert.Equal(t, SignedOut, Check(context.Background(), "claude", bin), "the temp folder has none")
	assert.Equal(t, SignedOut, CheckIn(context.Background(), "claude", bin, filepath.Join(repo, "missing")), "a folder that is not there is the temp folder")

	// the answers are kept per folder, so one folder's answer is never another's
	assert.NoError(t, RequireIn(context.Background(), "claude", bin, repo))
	assert.True(t, IsNotSignedIn(RequireIn(context.Background(), "claude", bin, other)))
	assert.NoError(t, RequireIn(context.Background(), "claude", bin, repo), "still signed in for the first folder")
	Forget("claude", bin)
	assert.True(t, IsNotSignedIn(RequireFreshIn(context.Background(), "claude", bin, other)))
	assert.NoError(t, RequireFreshIn(context.Background(), "claude", bin, repo))
}
