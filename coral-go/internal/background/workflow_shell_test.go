package background

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithShellDirsAddsTheShellsFoldersAfterThePath(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "usr", "bin")
	mingw := filepath.Join(root, "mingw64", "bin")
	assert.NoError(t, os.MkdirAll(mingw, 0o755))
	sh := filepath.Join(bin, "sh.exe")
	env := withShellDirs([]string{"A=1", "PATH=x", "B=2"}, sh)
	got := env[len(env)-1]
	assert.Equal(t, "PATH=x"+string(os.PathListSeparator)+bin+string(os.PathListSeparator)+mingw, got)
	assert.Equal(t, []string{"A=1", "PATH=x", "B=2"}, env[:3], "the input is not changed")
	assert.Equal(t, []string{"A=1"}, withShellDirs([]string{"A=1"}, "sh"), "a bare sh adds nothing")
	assert.True(t, strings.HasPrefix(withShellDirs(nil, sh)[0], "PATH="+bin), "no PATH yet")
}

func TestNoShellErrorSaysWhatToInstall(t *testing.T) {
	assert.Contains(t, errNoWorkflowShell.Error(), "workflow shell steps need Git for Windows")
	assert.Contains(t, errNoWorkflowShell.Error(), "restart Coral")
}
