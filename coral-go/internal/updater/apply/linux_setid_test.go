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

	for name, mode := range map[string]os.FileMode{"setuid": os.ModeSetuid | 0o755, "setgid": os.ModeSetgid | 0o755, "sticky": os.ModeSticky | 0o644} {
		require.NoError(t, os.Chmod(f, mode), name)
		_, err = plainTree(root)
		assert.Error(t, err, name)
	}
	require.NoError(t, os.Chmod(f, 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(root, "go", "link")))
	_, err = plainTree(root)
	assert.Error(t, err, "a link")
}
