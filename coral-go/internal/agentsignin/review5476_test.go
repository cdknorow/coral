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
