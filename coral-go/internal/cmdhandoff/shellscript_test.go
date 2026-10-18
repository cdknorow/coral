//go:build !windows

package cmdhandoff

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4627: the one script handoff the tmux and PTY launch paths share. The script is private, named
// as Sweep expects, removes itself when sourced, runs the command, and the cleanup removes one
// that was never sourced; a temp folder whose name holds a quote is still one shell word.
func TestShellScriptHandoff(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "it's a folder")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	t.Setenv("TMPDIR", dir)
	out := filepath.Join(dir, "out.txt")
	command := "printf '%s' 'ran' > " + Quote(out)

	inv, cleanup, err := ShellScript(command)
	require.NoError(t, err)
	scripts, _ := filepath.Glob(filepath.Join(dir, "coral-command-*.sh"))
	require.Len(t, scripts, 1)
	assert.Regexp(t, `^coral-command-[0-9]+\.sh$`, filepath.Base(scripts[0]), "a name Sweep removes")
	info, err := os.Stat(scripts[0])
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "private")
	assert.True(t, strings.HasPrefix(inv, ". '"), "a source command with one quoted path: %s", inv)
	assert.Less(t, len(inv), InlineLimit)

	// Sourced by a shell: it deletes itself, then runs the command.
	require.NoError(t, exec.Command("sh", "-c", inv).Run())
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "ran", string(got))
	_, err = os.Stat(scripts[0])
	assert.True(t, os.IsNotExist(err), "the script removed itself")
	cleanup() // already gone: harmless

	// One that was never sourced is removed by the cleanup.
	_, cleanup, err = ShellScript("true")
	require.NoError(t, err)
	left, _ := filepath.Glob(filepath.Join(dir, "coral-command-*.sh"))
	require.Len(t, left, 1)
	cleanup()
	left, _ = filepath.Glob(filepath.Join(dir, "coral-command-*.sh"))
	assert.Empty(t, left)
}

func TestShellScriptFailsCleanlyWithoutATempFolder(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	inv, cleanup, err := ShellScript("true")
	assert.Error(t, err)
	assert.Empty(t, inv)
	assert.NotNil(t, cleanup, "a cleanup is always safe to call")
	cleanup()
}

func TestQuote(t *testing.T) {
	assert.Equal(t, `'plain'`, Quote("plain"))
	assert.Equal(t, `'it'\''s'`, Quote("it's"))
	assert.Equal(t, `''`, Quote(""))
}
