package background

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3638: a path template in a shell command expands with forward slashes, which Git's sh on
// Windows keeps (it takes the backslashes of an unquoted C:\Users\... as escapes); an agent
// prompt keeps the native path. On Unix both are the same.
func TestPathTemplatesInShellCommandsUseForwardSlashes(t *testing.T) {
	native := filepath.Join("C:", "runs", "1", "step_0", "stdout.txt")
	env := []string{"CORAL_PREV_STDOUT=" + native, "CORAL_STEP_0_DIR=" + filepath.Dir(native)}
	got, err := expandShellTemplates("cat {{prev_stdout}} {{step_0_dir}}", env)
	require.NoError(t, err)
	assert.Equal(t, "cat "+filepath.ToSlash(native)+" "+filepath.ToSlash(filepath.Dir(native)), got)
	assert.NotContains(t, got, `\`)
	assert.Equal(t, "see "+native, expandTemplates("see {{prev_stdout}}", env), "a prompt keeps the native path")
}

// End to end: a later step reads an earlier one's files through the path templates.
func TestStepsReadEarlierOutputThroughPathTemplates(t *testing.T) {
	final, repo, _ := runWorkflowSteps(t, []StepDef{
		{Name: "make", Type: "shell", Command: "printf hello"},
		{Name: "use", Type: "shell", Command: "cat {{prev_stdout}} > a.txt && cat {{step_0_stdout}} > b.txt && ls {{prev_dir}} > c.txt"},
	})
	assert.Equal(t, "completed", final.Status, "%v %s", final.ErrorMsg, final.StepResults)
	for _, f := range []string{"a.txt", "b.txt"} {
		got, err := os.ReadFile(filepath.Join(repo, f))
		require.NoError(t, err, f)
		assert.Equal(t, "hello", string(got), f)
	}
	ls, err := os.ReadFile(filepath.Join(repo, "c.txt"))
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(ls), "stdout.txt"), "ls {{prev_dir}}: %q", ls)
}
