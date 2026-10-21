//go:build windows

package routes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QA's #28 C2: fileIdentity and fileLinks were stubs on Windows, so hard links to one diff object
// counted as separate files and the prune swept an object other diffs still linked.
func TestWindowsHardLinksShareAnIdentityAndCountLinks(t *testing.T) {
	dir := t.TempDir()
	a, b, other := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "other")
	require.NoError(t, os.WriteFile(a, []byte("diff --git a/x b/x\n"), 0o644))
	require.NoError(t, os.WriteFile(other, []byte("diff --git a/x b/x\n"), 0o644))
	fiA, err := os.Lstat(a)
	require.NoError(t, err)

	assert.EqualValues(t, 1, fileLinks(a, fiA), "one name")
	require.NoError(t, os.Link(a, b))
	fiA, _ = os.Lstat(a)
	fiB, _ := os.Lstat(b)
	fiO, _ := os.Lstat(other)

	assert.EqualValues(t, 2, fileLinks(a, fiA))
	assert.EqualValues(t, 2, fileLinks(b, fiB))
	ka, okA := fileIdentity(a, fiA)
	kb, okB := fileIdentity(b, fiB)
	ko, okO := fileIdentity(other, fiO)
	require.True(t, okA && okB && okO)
	assert.Equal(t, ka, kb, "two names of one file")
	assert.NotEqual(t, ka, ko, "equal bytes in another file are another file")

	require.NoError(t, os.Remove(b))
	assert.EqualValues(t, 1, fileLinks(a, fiA), "back to one name")
}

// A missing file, or a link/reparse point, gives no identity, and a link count that keeps it
// (2, never 1): the sweep removes only what has exactly one link.
func TestWindowsFileIdentityFailsSafe(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone")
	_, ok := fileIdentity(missing, nil)
	assert.False(t, ok)
	assert.EqualValues(t, 2, fileLinks(missing, nil))

	target := filepath.Join(dir, "t")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o644))
	link := filepath.Join(dir, "l")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlink privilege: %v", err)
	}
	_, ok = fileIdentity(link, nil)
	assert.False(t, ok, "a symlink is not the file it points at")
	assert.EqualValues(t, 2, fileLinks(link, nil))
}
