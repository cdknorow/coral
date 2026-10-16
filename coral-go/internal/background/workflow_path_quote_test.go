package background

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3930 (Security's #3927 finding 7): a path template in a shell step is quoted for where it
// sits, so a data folder with an apostrophe, a space, $(...), a double quote or a backtick names
// exactly that file and runs nothing, written bare, inside double quotes or inside single quotes.
func TestShellStepPathTemplatesAreQuoted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh quoting; Windows steps run in Git's sh with forward-slash paths, covered elsewhere")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	run := filepath.Join(t.TempDir(), "o'brien $(touch pwned) \"q\" `touch tick` x")
	require.NoError(t, os.MkdirAll(run, 0o700))
	out := filepath.Join(run, "stdout.txt")
	require.NoError(t, os.WriteFile(out, []byte("hello"), 0o600))
	env := []string{"CORAL_PREV_STDOUT=" + out, "CORAL_WORKFLOW_RUN_DIR=" + run}
	for _, cmd := range []string{
		"cat {{prev_stdout}}",
		`cat "{{prev_stdout}}"`,
		`cat '{{prev_stdout}}'`,
		"cat {{run_dir}}/stdout.txt",
		`cat "{{run_dir}}/stdout.txt"`,
		`cat '{{run_dir}}'/stdout.txt`,
	} {
		expanded, err := expandShellTemplates(cmd, env)
		require.NoError(t, err)
		cwd := t.TempDir()
		c := exec.Command(sh, "-c", expanded)
		c.Dir = cwd
		got, err := c.CombinedOutput()
		assert.NoError(t, err, "%s -> %s: %s", cmd, expanded, got)
		assert.Equal(t, "hello", string(got), "%s -> %s", cmd, expanded)
		for _, f := range []string{"pwned", "tick"} {
			_, err := os.Stat(filepath.Join(cwd, f))
			assert.True(t, os.IsNotExist(err), "%s ran from %s", f, cmd)
		}
	}
	// In an agent prompt the path stays plain text.
	assert.Equal(t, "read "+out, expandTemplatesFor("read {{prev_stdout}}", env, false))
}
