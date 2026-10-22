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

// #5911: an agent's launch line is always a script, whatever its length. The shell is typed one short line that sources it; the script erases the screen and the scrollback first, then
// holds what the typed line used to hold, and removes itself unless CORAL_KEEP_LAUNCH_SCRIPTS is set.
func TestQuietShellScriptIsShortPrivateClearsFirstAndRemovesItself(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv(KeepEnv, "")
	out := filepath.Join(dir, "out.txt")
	command := "printf '%s' 'ran' > " + Quote(out) // far under InlineLimit: a script all the same

	inv, cleanup, err := QuietShellScript(command)
	require.NoError(t, err)
	scripts, _ := filepath.Glob(filepath.Join(dir, "coral-command-*.sh"))
	require.Len(t, scripts, 1)
	assert.Regexp(t, `^coral-command-[0-9]+\.sh$`, filepath.Base(scripts[0]), "a name Sweep removes")
	info, err := os.Stat(scripts[0])
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "private")
	assert.True(t, strings.HasPrefix(inv, ". '"), inv)
	assert.Less(t, len(inv), 200, "the line typed is short and names only the script")
	assert.Equal(t, ". "+Quote(scripts[0]), inv)

	body, err := os.ReadFile(scripts[0])
	require.NoError(t, err)
	text := string(body)
	rm := strings.Index(text, "rm -f -- ")
	clear := strings.Index(text, ClearScreen)
	run := strings.Index(text, command)
	assert.True(t, rm >= 0 && rm < clear && clear < run, "removes itself, then erases the screen, then runs the command:\n%s", text)
	assert.Contains(t, ClearScreen, `\033[3J`, "the scrollback goes too")

	// sourced: the screen-clearing bytes are printed, the command runs, the script is gone
	cmd := exec.Command("sh", "-c", inv)
	printed, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(printed), "\x1b[2J\x1b[3J\x1b[H")
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "ran", string(got))
	_, err = os.Stat(scripts[0])
	assert.True(t, os.IsNotExist(err), "the script removed itself")
	cleanup() // already gone: harmless

	// two launches never share a name
	_, c1, err := QuietShellScript("true")
	require.NoError(t, err)
	_, c2, err := QuietShellScript("true")
	require.NoError(t, err)
	two, _ := filepath.Glob(filepath.Join(dir, "coral-command-*.sh"))
	assert.Len(t, two, 2)
	c1()
	c2() // a delivery that failed removes its script
	none, _ := filepath.Glob(filepath.Join(dir, "coral-command-*.sh"))
	assert.Empty(t, none, "removed on the failure path")
}

func TestQuietShellScriptIsKeptWhenAskedForDebugging(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv(KeepEnv, "1")
	inv, cleanup, err := QuietShellScript("true")
	require.NoError(t, err)
	defer cleanup()
	require.NoError(t, exec.Command("sh", "-c", inv).Run())
	scripts, _ := filepath.Glob(filepath.Join(dir, "coral-command-*.sh"))
	require.Len(t, scripts, 1, "kept after it ran")
	body, _ := os.ReadFile(scripts[0])
	assert.NotContains(t, string(body), "rm -f --")
	assert.Contains(t, string(body), "true")
	// the plain handoff is not affected by the switch
	_, c2, err := ShellScript("true")
	require.NoError(t, err)
	defer c2()
	all, _ := filepath.Glob(filepath.Join(dir, "coral-command-*.sh"))
	for _, p := range all {
		if p == scripts[0] {
			continue
		}
		b, _ := os.ReadFile(p)
		assert.Contains(t, string(b), "rm -f --", "ShellScript always removes itself")
	}
}
