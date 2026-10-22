//go:build windows

package ptymanager

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secfile/secfiletest"
)

// #3690 (#3689 finding 2): with the temp folder under a name holding an apostrophe (a profile
// like C:\Users\O'Brien), PowerShell runs the handoff, and the script removes itself.
func TestWindowsCommandHandoffWithAnApostropheInThePath(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "O'Brien")
	require.NoError(t, os.MkdirAll(tmp, 0o700))
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	inv, cleanup, err := commandInvocation("Write-Output coral-handoff-ok", false)
	require.NoError(t, err)
	defer cleanup()
	require.True(t, strings.Contains(inv, "O''Brien"), inv)

	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", inv).CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Contains(t, string(out), "coral-handoff-ok")
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	assert.Empty(t, entries, "the script removed itself")
}

// #3906 (sweep item 8): the handoff script, which holds the launch line, has an access list of
// its own (this user and SYSTEM), not the temp folder's.
func TestWindowsCommandHandoffIsPrivate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	_, cleanup, err := commandInvocation("Write-Output coral-handoff-ok", false)
	require.NoError(t, err)
	defer cleanup()
	scripts, err := filepath.Glob(filepath.Join(tmp, "coral-command-*.ps1"))
	require.NoError(t, err)
	require.Len(t, scripts, 1)
	secfiletest.RequirePrivate(t, scripts[0])
}
