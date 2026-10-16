package agent

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookEchoEnv makes a copy of this test binary print its own path and exit (TestMain).
const hookEchoEnv = "CORAL_TEST_HOOK_ECHO"

func copyTestBinary(t *testing.T, to string) {
	t.Helper()
	src, err := os.Open(os.Args[0])
	require.NoError(t, err)
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	require.NoError(t, err)
	_, err = io.Copy(dst, src)
	require.NoError(t, err)
	require.NoError(t, dst.Close())
}

// #3660 item 6: a hook command names Coral's tool by its absolute path, so a program of the same
// name in the agent's folder or earlier on PATH (a repository's decoy) never runs instead. Both
// the real tool and the decoy are copies of this test binary that print their own path; the
// command runs through sh or bash, and on Windows through cmd.exe too, from the decoy's folder
// with the decoy first on PATH.
func TestHookCommandRunsTheToolsFolder(t *testing.T) {
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	tools := filepath.Join(t.TempDir(), "Coral Tools") // a space, as in "Program Files"
	require.NoError(t, os.Mkdir(tools, 0o700))
	real := filepath.Join(tools, "coral-hook-agentic-state"+exe)
	copyTestBinary(t, real)
	work := t.TempDir()
	decoy := filepath.Join(work, "coral-hook-agentic-state"+exe)
	copyTestBinary(t, decoy)
	t.Setenv(HookDirEnv, tools)

	cmd := ResolveHookCommand("coral-hook-agentic-state --hooks-rev 2")
	require.True(t, strings.HasPrefix(cmd, `"`), "by quoted absolute path: %s", cmd)
	require.True(t, strings.HasSuffix(cmd, `" --hooks-rev 2`), cmd)

	env := append(os.Environ(), hookEchoEnv+"=1", "PATH="+work+string(os.PathListSeparator)+os.Getenv("PATH"))
	want, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)
	decoyPath, err := filepath.EvalSymlinks(decoy)
	require.NoError(t, err)
	ranFrom := func(shell string, c *exec.Cmd) string {
		t.Helper()
		c.Dir, c.Env = work, env
		out, err := c.CombinedOutput()
		require.NoError(t, err, "%s: %s", shell, out)
		got, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
		require.NoError(t, err, "%s printed %q", shell, out)
		return got
	}
	check := func(shell string, c *exec.Cmd) {
		t.Helper()
		assert.Equal(t, want, ranFrom(shell, c), "%s ran the tools folder's hook, not the decoy", shell)
	}
	ran := 0
	for _, sh := range []string{"sh", "bash"} {
		if p, err := exec.LookPath(sh); err == nil && !strings.EqualFold(filepath.Dir(p), os.Getenv("SystemRoot")+`\System32`) {
			// The setup is real: the bare name runs the decoy.
			assert.Equal(t, decoyPath, ranFrom(sh, exec.Command(p, "-c", "coral-hook-agentic-state --hooks-rev 2")), "%s: the decoy is first", sh)
			check(sh, exec.Command(p, "-c", cmd))
			ran++
		}
	}
	if c := cmdExe(cmd); c != nil {
		check("cmd.exe", c)
		ran++
	}
	if ran == 0 {
		t.Skip("no shell to run a hook command")
	}

	// Without a tools folder (or without the tool in it) the command stays the bare name.
	t.Setenv(HookDirEnv, t.TempDir())
	assert.Equal(t, "coral-hook-agentic-state --hooks-rev 2", ResolveHookCommand("coral-hook-agentic-state --hooks-rev 2"))
	// Commands that aren't Coral's tools are never changed.
	assert.Equal(t, "my-hook --x", ResolveHookCommand("my-hook --x"))
}
