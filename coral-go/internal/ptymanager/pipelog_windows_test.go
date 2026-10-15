package ptymanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3601 W17 on Windows (#3615): a hard link planted at an agent log's name is refused by the
// pty backend's append and by the respawn reset, with the count read from the handle.
func TestAgentLogRefusesAHardLinkOnWindows(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(outside, []byte("keep me"), 0o600))
	p := filepath.Join(dir, "planted.log")
	require.NoError(t, os.Link(outside, p))
	f, err := openLogAppend(p)
	if err == nil {
		f.Close()
	}
	assert.Error(t, err, "the pty backend's append refuses it")
	assert.Error(t, resetLog(p), "resetLog refuses it")
	b, _ := os.ReadFile(outside)
	assert.Equal(t, "keep me", string(b))

	f, err = openLogAppend(filepath.Join(dir, "fresh.log"))
	require.NoError(t, err)
	f.Close()
}
