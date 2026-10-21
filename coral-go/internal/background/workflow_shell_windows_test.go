//go:build windows

package background

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeGit(t *testing.T) (root string) {
	root = t.TempDir()
	for _, p := range []string{filepath.Join("usr", "bin", "sh.exe"), filepath.Join("cmd", "git.exe")} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, p), nil, 0o755))
	}
	return root
}

func TestFindWorkflowShellFromGitOnPath(t *testing.T) {
	root := fakeGit(t)
	t.Setenv("PATH", filepath.Join(root, "cmd"))
	t.Setenv("ProgramFiles", t.TempDir())
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("ProgramW6432", "")
	t.Setenv("LOCALAPPDATA", t.TempDir())
	assert.Equal(t, filepath.Join(root, "usr", "bin", "sh.exe"), findWorkflowShell())
}

func TestFindWorkflowShellFromProgramFiles(t *testing.T) {
	root := fakeGit(t)
	pf := t.TempDir()
	require.NoError(t, os.Rename(root, filepath.Join(pf, "Git")))
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ProgramFiles", pf)
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("ProgramW6432", "")
	t.Setenv("LOCALAPPDATA", t.TempDir())
	assert.Equal(t, filepath.Join(pf, "Git", "usr", "bin", "sh.exe"), findWorkflowShell())
}

func TestFindWorkflowShellFindsNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ProgramFiles", t.TempDir())
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("ProgramW6432", "")
	t.Setenv("LOCALAPPDATA", t.TempDir())
	assert.Equal(t, "", findWorkflowShell())
}

// A standard Git install leaves only Git\cmd on the Windows PATH: external commands in a step still work.
func TestStepsRunExternalCommandsWithOnlyGitCmdOnPath(t *testing.T) {
	sh, err := workflowShell()
	require.NoError(t, err)
	if !filepath.IsAbs(sh) {
		t.Skip("no Git for Windows")
	}
	env := withShellDirs([]string{"PATH=" + filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(sh))), "cmd")}, sh)
	cmd := exec.Command(sh, "-c", "uname -s && echo hi | cat")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "hi")
}
