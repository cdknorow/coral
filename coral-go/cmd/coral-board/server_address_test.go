package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With no CORAL_URL, CORAL_HOST or CORAL_PORT (a person's plain terminal) coral-board finds the running Coral by the address in its data folder's lock file; a lock that names another computer is ignored by config.LocalServerURL (its own test: no command is sent to the default port here, where a Coral may be running). The board state here has no server_url, so the lock decides.
func TestBoardFindsTheRunningCoralFromItsLockFile(t *testing.T) {
	srv := newRecordingServer(t)
	dir := t.TempDir()
	state, _ := json.Marshal(boardState{Project: "team", JobTitle: "Tester"})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "board_state_help-test.json"), state, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "coral-server.lock"), []byte("4242\n"+srv.URL+"\n"), 0o600))

	res := runBoardNoURL(t, dir, "post", "found by the lock")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, "found by the lock", postedContent(t, srv))
}

// runBoardNoURL runs coral-board with CORAL_URL unset.
func runBoardNoURL(t *testing.T, dataDir string, args ...string) boardRun {
	t.Helper()
	return runBoardInput(t, "", dataDir, nil, args...)
}
