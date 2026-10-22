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

// Only Git's own layouts count: an sh.exe beside or above the install (for C:\Git\cmd\git.exe, C:\usr\bin\sh.exe, a
// folder every user can write) is not Git's shell, and neither is one two levels above a mingw64\bin git.
func TestFindWorkflowShellIgnoresAPlantedShellAboveTheInstall(t *testing.T) {
	for _, layout := range [][]string{{"cmd"}, {"bin"}, {"mingw64", "bin"}} {
		base := t.TempDir()
		planted := filepath.Join(base, "usr", "bin", "sh.exe")
		require.NoError(t, os.MkdirAll(filepath.Dir(planted), 0o755))
		require.NoError(t, os.WriteFile(planted, nil, 0o755))
		install := filepath.Join(base, "Git") // Git without a usr\bin\sh.exe of its own
		gitDir := filepath.Join(append([]string{install}, layout...)...)
		require.NoError(t, os.MkdirAll(gitDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "git.exe"), nil, 0o755))
		t.Setenv("PATH", gitDir)
		t.Setenv("ProgramFiles", t.TempDir())
		t.Setenv("ProgramFiles(x86)", "")
		t.Setenv("ProgramW6432", "")
		t.Setenv("LOCALAPPDATA", t.TempDir())
		assert.Equal(t, "", findWorkflowShell(), "%v", layout)
	}
}

// The three layouts Git for Windows ships are found, from git on PATH.
func TestFindWorkflowShellKnownLayouts(t *testing.T) {
	for _, layout := range [][]string{{"cmd"}, {"bin"}, {"mingw64", "bin"}} {
		root := t.TempDir()
		sh := filepath.Join(root, "usr", "bin", "sh.exe")
		gitDir := filepath.Join(append([]string{root}, layout...)...)
		for _, p := range []string{sh, filepath.Join(gitDir, "git.exe")} {
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, nil, 0o755))
		}
		t.Setenv("PATH", gitDir)
		t.Setenv("ProgramFiles", t.TempDir())
		t.Setenv("ProgramFiles(x86)", "")
		t.Setenv("ProgramW6432", "")
		t.Setenv("LOCALAPPDATA", t.TempDir())
		assert.Equal(t, sh, findWorkflowShell(), "%v", layout)
	}
}

func TestGitInstallRoot(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Git\cmd\git.exe`:              `C:\Git`,
		`C:\Git\bin\git.exe`:              `C:\Git`,
		`C:\Git\mingw64\bin\git.exe`:      `C:\Git`,
		`C:\Git\MinGW64\BIN\git.exe`:      `C:\Git`,
		`C:\cmd\git.exe`:                  ``, // the drive root
		`C:\bin\git.exe`:                  ``,
		`C:\mingw64\bin\git.exe`:          ``, // two up is the drive root
		`C:\Git\mingw64\git.exe`:          ``,
		`C:\Tools\git.exe`:                ``,
		`C:\Git\libexec\git-core\git.exe`: ``,
	} {
		assert.Equal(t, want, gitInstallRoot(in), in)
	}
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
	// System32 follows Git\cmd as on a real machine: its find, sort and timeout must not shadow the POSIX ones.
	sys32 := filepath.Join(os.Getenv("SystemRoot"), "System32")
	env := withShellDirs([]string{"PATH=" + filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(sh))), "cmd") + ";" + sys32}, sh)
	cmd := exec.Command(sh, "-c", "uname -s && echo hi | cat && find . -maxdepth 0 -name . && printf 'a\nb\n' | sort -r | head -1")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "hi")
	assert.Contains(t, string(out), "b", "sort -r ran")
}
