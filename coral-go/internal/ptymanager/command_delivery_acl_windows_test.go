//go:build windows

package ptymanager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/secfile"
)

// #25 G2B: the launch temp folder is private to this user and SYSTEM by its own access list (os.Chmod does
// nothing on Windows), and the handoff script is made in it, private from its creation.
func TestHandoffScriptIsMadeInThePrivateLaunchTempFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tmp")
	require.NoError(t, agent.SetTempDir(dir))
	t.Cleanup(func() { agent.ResetLaunchTempDirForTest() })

	invocation, cleanup, err := commandInvocation("Write-Output hi")
	require.NoError(t, err)
	t.Cleanup(cleanup)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the script is in the launch temp folder")
	path := filepath.Join(dir, entries[0].Name())
	assert.Contains(t, invocation, path)
	assert.Regexp(t, `^coral-command-[0-9]+\.ps1$`, entries[0].Name(), "a name cmdhandoff.Sweep recognises")

	ok, err := secfile.Explicit(path)
	require.NoError(t, err)
	assert.True(t, ok, "the script has its own protected access list")
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	assert.NoError(t, secfile.CheckOpen(f), "only this user and SYSTEM can read the script")

	d, err := os.Open(dir)
	require.NoError(t, err)
	defer d.Close()
	ok, err = secfile.Explicit(dir)
	require.NoError(t, err)
	assert.True(t, ok, "the folder has its own protected access list, not its parent's")

	// A file made in the folder by anything else is private by inheritance.
	other := filepath.Join(dir, "other.txt")
	require.NoError(t, os.WriteFile(other, []byte("x"), 0o600))
	o, err := os.Open(other)
	require.NoError(t, err)
	defer o.Close()
	assert.NoError(t, secfile.CheckOpen(o))
}
