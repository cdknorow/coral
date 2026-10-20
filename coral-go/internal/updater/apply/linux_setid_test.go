//go:build !windows

// Not built on Windows: these test the Linux and macOS apply (POSIX file modes, renames over open files, symlinks, a staged record read back by path); the Windows apply is a separate piece (journal, MSI hand-off), #5395.

package apply

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5341: a licence file whose mode was changed to set-id after the unpack has the same bytes, so the hash recheck cannot see it: the walk must.
func TestAPlainTreeRefusesASetIDFileAndALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("set-id bits and unprivileged links are not a Windows thing")
	}
	root := filepath.Join(t.TempDir(), "licenses")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "go", "a"), 0o755))
	f := filepath.Join(root, "go", "a", "LICENSE")
	require.NoError(t, os.WriteFile(f, []byte("mit"), 0o644))
	files, err := plainTree(root)
	require.NoError(t, err)
	assert.Equal(t, []string{"go/a/LICENSE"}, files)

	// A host may drop a bit the user is not entitled to (macOS drops setgid on a file whose group the user is not in): a bit that did not stick
	// is not a test of this walk, so it is skipped by name, and the test as a whole is skipped when none stuck, never passed for nothing.
	exercised := 0
	for name, mode := range map[string]os.FileMode{"setuid": os.ModeSetuid | 0o755, "setgid": os.ModeSetgid | 0o755, "sticky": os.ModeSticky | 0o644} {
		require.NoError(t, os.Chmod(f, mode), name)
		if fi, serr := os.Lstat(f); serr != nil || fi.Mode()&mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 {
			t.Logf("%s did not stick on this host: not tested here", name)
			continue
		}
		exercised++
		_, err = plainTree(root)
		assert.Error(t, err, name)
	}
	require.NoError(t, os.Chmod(f, 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(root, "go", "link")))
	_, err = plainTree(root)
	assert.Error(t, err, "a link")
	if exercised == 0 { // the link was tested; the set-id bits could not be on this host
		t.Skip("this host keeps none of the set-id bits on a file")
	}
}
