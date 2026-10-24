//go:build !windows

package cmdhandoff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TMPDIR naming a folder that is gone (Codex Test's #6953): the script is written to the fallback folder, Coral's own, instead of the launch failing; with no fallback the error stays an error (#6978).
func TestShellScriptFallsBackWhenTheTempFolderIsUnusable(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "deleted"))
	t.Cleanup(func() { fallbackDir.Store(nil) })

	fallbackDir.Store(nil)
	_, _, err := ShellScript("true")
	require.Error(t, err, "no fallback: the failure is reported")

	fallback := t.TempDir()
	SetFallbackDir(func() string { return fallback })
	inv, cleanup, err := ShellScript("true")
	require.NoError(t, err)
	t.Cleanup(cleanup)
	scripts, _ := filepath.Glob(filepath.Join(fallback, "coral-command-*.sh"))
	require.Len(t, scripts, 1, "the script is in the fallback folder")
	assert.Contains(t, inv, scripts[0])
	info, err := os.Stat(scripts[0])
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "private")
}
