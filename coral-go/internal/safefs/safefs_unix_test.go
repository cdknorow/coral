//go:build !windows

package safefs

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// Security's SHOULD 1: a FIFO where the file goes fails the write at once (it used to block
// in the open until a reader came).
func TestWriteFileFIFOFailsPromptly(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, "fifo"), 0o600))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	done := make(chan error, 1)
	go func() { done <- r.WriteFile("fifo", []byte("x"), 0o644) }()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrNotRegular)
	case <-time.After(5 * time.Second):
		t.Fatal("writing to a FIFO blocked")
	}
}

// Security's SHOULD 2: a folder swapped for a link that stays INSIDE the root (into
// .git/hooks) after the caller's checks, right before the open. The path is decided again
// after the open, so the deny check sees .git and refuses; an existing hook is unchanged and a
// new one the open created is removed again.
func TestWriteFileUnlessSeesASwapAfterTheCheck(t *testing.T) {
	root := t.TempDir()
	hooks := filepath.Join(root, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooks, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	denyGit := func(fresh string) bool {
		for _, c := range strings.Split(filepath.ToSlash(fresh), "/") {
			if strings.EqualFold(c, ".git") {
				return true
			}
		}
		return false
	}
	// A RELATIVE link (sub -> .git/hooks) stays inside the root, so os.Root follows it: this is
	// the race the deny-after-open closes. (An absolute link is refused by os.Root itself.)
	swapped := false
	testBeforeOpen = func(string) {
		if !swapped {
			swapped = true
			require.NoError(t, os.Remove(filepath.Join(root, "sub")))
			require.NoError(t, os.Symlink(filepath.Join(".git", "hooks"), filepath.Join(root, "sub")))
		}
	}
	t.Cleanup(func() { testBeforeOpen = nil })

	assert.ErrorIs(t, r.WriteFileUnless(filepath.Join("sub", "post-checkout"), []byte("touch PWNED"), 0o644, denyGit), ErrDenied)
	b, _ := os.ReadFile(filepath.Join(hooks, "post-checkout"))
	assert.Equal(t, "#!/bin/sh\n", string(b), "the existing hook is unchanged")

	assert.ErrorIs(t, r.WriteFileUnless(filepath.Join("sub", "pre-push"), []byte("touch PWNED"), 0o644, denyGit), ErrDenied)
	_, err = os.Stat(filepath.Join(hooks, "pre-push"))
	assert.True(t, os.IsNotExist(err), "a hook the open created is removed again")

	// Without the deny check (the local dashboard), the root still keeps it inside: this write
	// lands in the hooks folder only because no deny was asked for.
	testBeforeOpen = nil

	// An absolute link, even into the root, is refused by os.Root itself.
	require.NoError(t, os.Remove(filepath.Join(root, "sub")))
	require.NoError(t, os.Symlink(hooks, filepath.Join(root, "sub")))
	assert.Error(t, r.WriteFileUnless(filepath.Join("sub", "post-merge"), []byte("touch PWNED"), 0o644, denyGit))
	_, err = os.Stat(filepath.Join(hooks, "post-merge"))
	assert.True(t, os.IsNotExist(err))

	// Without the swap, an ordinary write through the same call works.
	require.NoError(t, r.MkdirAll("plain", 0o755))
	require.NoError(t, r.WriteFileUnless(filepath.Join("plain", "f.txt"), []byte("ok"), 0o644, denyGit))
}

// ReadFileOneLink (#3672): a hard link to a file outside the root is refused for a read too; a
// FIFO is refused at once; a plain file reads, and a too-big one is ErrTooLarge.
func TestReadFileOneLink(t *testing.T) {
	root := t.TempDir()
	_, secret := outside(t)
	require.NoError(t, os.Link(secret, filepath.Join(root, "notes.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "plain.txt"), []byte("hello"), 0o644))
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, "fifo"), 0o600))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()

	_, err = r.ReadFileOneLink("notes.txt", 1<<20)
	assert.ErrorIs(t, err, ErrLinked)
	b, err := r.ReadFileOneLink("plain.txt", 1<<20)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
	_, err = r.ReadFileOneLink("plain.txt", 3)
	assert.ErrorIs(t, err, ErrTooLarge)

	done := make(chan error, 1)
	go func() { _, err := r.ReadFileOneLink("fifo", 1<<20); done <- err }()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrNotRegular)
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
}
