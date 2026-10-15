//go:build !windows

package safefs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outside makes a folder outside the root with a file whose content must not change.
func outside(t *testing.T) (dir, secret string) {
	t.Helper()
	dir = t.TempDir()
	secret = filepath.Join(dir, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("SECRET"), 0o600))
	return dir, secret
}

func secretIntact(t *testing.T, dir, secret string) {
	t.Helper()
	b, _ := os.ReadFile(secret)
	assert.Equal(t, "SECRET", string(b), "the outside file is unchanged")
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "nothing new outside")
}

func TestWithinSeesSymlinkedAncestors(t *testing.T) {
	root := t.TempDir()
	out, _ := outside(t)
	require.NoError(t, os.Symlink(out, filepath.Join(root, "link")))
	for _, p := range []string{"link", "link/secret.txt", "link/new/deeper.txt"} {
		_, err := Within(root, p)
		assert.ErrorIs(t, err, ErrOutside, p)
	}
	// A symlinked root resolves; paths inside it are fine.
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(root, alias))
	rel, err := Within(alias, "x.txt")
	require.NoError(t, err)
	assert.Equal(t, "x.txt", rel)
}

func TestRootRefusesLinksOut(t *testing.T) {
	root := t.TempDir()
	out, secret := outside(t)
	require.NoError(t, os.Symlink(secret, filepath.Join(root, "file-link")))
	require.NoError(t, os.Symlink(out, filepath.Join(root, "dir-link")))
	require.NoError(t, os.Symlink("/dev/zero", filepath.Join(root, "zero")))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()

	for _, rel := range []string{"file-link", "dir-link/secret.txt", "zero"} {
		_, err := r.ReadFile(rel, 1<<20)
		assert.Error(t, err, "read %s", rel)
		assert.Error(t, r.WriteFile(rel, []byte("pwned"), 0o644), "write %s", rel)
	}
	assert.Error(t, r.MkdirAll(filepath.Join("dir-link", "made"), 0o755))
	_, err = r.CreateNew(filepath.Join("dir-link", "new.txt"), 0o600)
	assert.Error(t, err)
	secretIntact(t, out, secret)
}

// (c) a hard link is another name for a file outside the root: never written through.
func TestRootRefusesWritingAHardLink(t *testing.T) {
	root := t.TempDir()
	out, secret := outside(t)
	require.NoError(t, os.Link(secret, filepath.Join(root, "hard.txt")))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	assert.ErrorIs(t, r.WriteFile("hard.txt", []byte("pwned"), 0o644), ErrLinked)
	_, err = r.CreateNew("hard.txt", 0o600)
	assert.Error(t, err)
	secretIntact(t, out, secret)
}

// (b) a FIFO is refused at once (non-blocking open, then not a regular file).
func TestRootFIFOAndDevice(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, "fifo"), 0o600))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	done := make(chan error, 1)
	go func() { _, err := r.ReadFile("fifo", 1<<20); done <- err }()
	select {
	case err := <-done:
		assert.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	// /dev/zero itself as a root's file: a device is never a regular file.
	dev, err := OpenRoot("/dev")
	require.NoError(t, err)
	defer dev.Close()
	_, err = dev.ReadFile("zero", 1<<20)
	assert.ErrorIs(t, err, ErrNotRegular)
}

// TOCTOU: a folder checked by Within and then swapped for a symlink out before the write. The
// root handle still keeps the write inside.
func TestSwappedAncestorAfterTheCheck(t *testing.T) {
	root := t.TempDir()
	out, secret := outside(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))
	rel, err := Within(root, "sub/secret.txt")
	require.NoError(t, err, "inside at the time of the check")
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	require.NoError(t, os.Remove(filepath.Join(root, "sub")))
	require.NoError(t, os.Symlink(out, filepath.Join(root, "sub")))
	assert.Error(t, r.WriteFile(rel, []byte("pwned"), 0o644), "the swapped ancestor isn't followed out")
	_, err = r.ReadFile(rel, 1<<20)
	assert.Error(t, err)
	secretIntact(t, out, secret)
}

// (d) a root deleted after it was opened: writes fail, nothing is made anywhere else.
func TestDeletedRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	require.NoError(t, os.Mkdir(root, 0o755))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	require.NoError(t, os.RemoveAll(root))
	assert.Error(t, r.WriteFile("x.txt", []byte("x"), 0o644))
	assert.Error(t, r.MkdirAll("d", 0o755))
	entries, _ := os.ReadDir(parent)
	assert.Empty(t, entries, "nothing was made in the root's parent")
	_, err = Within(root, "x.txt")
	assert.ErrorIs(t, err, ErrRootGone)
}

// A symlink that stays inside the root is still never written or read through as the last
// name: a repository's own link into .git/hooks must not turn a file write into a hook
// (#3596 W1).
func TestRootRefusesALinkInsideTheRootAsTheLastName(t *testing.T) {
	root := t.TempDir()
	hooks := filepath.Join(root, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooks, 0o755))
	hook := filepath.Join(hooks, "post-checkout")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(".git", "hooks", "post-checkout"), filepath.Join(root, "notes.txt")))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	assert.ErrorIs(t, r.WriteFile("notes.txt", []byte("touch PWNED"), 0o644), ErrNotRegular)
	_, err = r.ReadFile("notes.txt", 1<<20)
	assert.ErrorIs(t, err, ErrNotRegular)
	b, _ := os.ReadFile(hook)
	assert.Equal(t, "#!/bin/sh\n", string(b), "the hook is unchanged")
}
