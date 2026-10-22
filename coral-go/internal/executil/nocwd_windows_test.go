//go:build windows

package executil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withoutNoCwd(env []string) []string {
	var out []string
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); !strings.EqualFold(name, "NoDefaultCurrentDirectoryInExePath") {
			out = append(out, kv)
		}
	}
	return out
}

func TestWithNoCwdSearchSetsTheVariableOnceWhateverItsCase(t *testing.T) {
	got := WithNoCwdSearch([]string{"A=1", "nodefaultcurrentdirectoryinexepath=0", "B=2"})
	assert.Equal(t, []string{"A=1", "B=2", NoCwdSearchEnv}, got, "the old value is replaced, not doubled")
	assert.Equal(t, []string{NoCwdSearchEnv}, WithNoCwdSearch(nil))

	cmd := exec.Command("x")
	NoCwdSearch(cmd)
	assert.Contains(t, cmd.Env, NoCwdSearchEnv, "a command with no environment of its own gets this process's, plus the variable")
	assert.Greater(t, len(cmd.Env), 1)
}

// An npm-style shim (claude.cmd) runs another program by its bare name. Started with a repository as its working folder, cmd.exe looks there first, so a program planted in
// the repository runs as Coral; with NoCwdSearch it does not (#183). The planted file stands for node.exe/node.cmd; the shim calls a name nothing else on the machine has.
func TestAPlantedProgramInTheWorkingFolderDoesNotRunWithNoCwdSearch(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	shim := filepath.Join(root, "shim")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	require.NoError(t, os.MkdirAll(shim, 0o755))
	marker := filepath.Join(root, "planted-ran")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "coral_probe_tool.cmd"), []byte("@echo ran> \""+marker+"\"\r\n"), 0o755))
	claude := filepath.Join(shim, "claude.cmd")
	require.NoError(t, os.WriteFile(claude, []byte("@coral_probe_tool\r\n"), 0o755))

	run := func(harden bool) bool {
		_ = os.Remove(marker)
		cmd := exec.Command(claude)
		cmd.Dir = repo
		cmd.Env = withoutNoCwd(os.Environ()) // this test process may itself run under the variable (Coral gives it to its agents): the baseline needs it absent
		if harden {
			NoCwdSearch(cmd)
		}
		_ = cmd.Run() // the shim fails to find the tool when the folder is not searched: that is the point
		_, err := os.Stat(marker)
		return err == nil
	}
	if !run(false) {
		t.Skip("this cmd.exe does not search the working folder first (NoDefaultCurrentDirectoryInExePath is already set for this process)")
	}
	assert.False(t, run(true), "with NoCwdSearch the planted program is not run")
}
