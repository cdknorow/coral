//go:build !windows

package gitutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/safefs"
)

// #3597 (weak site W18): counting a new file's lines reads only a regular file: never through a
// symlink, never a FIFO (which blocked the git poller for every session) or a device like
// /dev/zero (which had no size to cap and read without end).
func TestNewFileLineCountReadsOnlyRegularFiles(t *testing.T) {
	wd := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(wd, name)
		require.NoError(t, os.WriteFile(p, []byte(s), 0o600))
		return p
	}
	assert.Equal(t, 2, NewFileLineCount(write("two.txt", "a\nb\n")))
	assert.Equal(t, 3, NewFileLineCount(write("three.txt", "a\nb\nc")))

	outside := filepath.Join(t.TempDir(), "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("1\n2\n3\n4\n"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(wd, "link.txt")))
	require.NoError(t, os.Symlink("/dev/zero", filepath.Join(wd, "zero")))
	require.NoError(t, syscall.Mkfifo(filepath.Join(wd, "fifo"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(wd, "fifo"), filepath.Join(wd, "fifo-link")))

	done := make(chan map[string]int, 1)
	go func() {
		got := map[string]int{}
		for _, name := range []string{"link.txt", "zero", "fifo", "fifo-link"} {
			got[name] = NewFileLineCount(filepath.Join(wd, name))
		}
		done <- got
	}()
	select {
	case got := <-done:
		for name, n := range got {
			assert.Equal(t, 0, n, name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("counting blocked on a FIFO or read /dev/zero without end")
	}

	// Inside a root, a symlinked folder on the way out isn't followed either.
	outDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outDir, "x.txt"), []byte("1\n2\n"), 0o600))
	require.NoError(t, os.Symlink(outDir, filepath.Join(wd, "outdir")))
	root, err := safefs.OpenRoot(wd)
	require.NoError(t, err)
	defer root.Close()
	assert.Equal(t, 0, NewFileLineCountIn(root, filepath.Join("outdir", "x.txt")))
	assert.Equal(t, 2, NewFileLineCountIn(root, "two.txt"))

	// Larger than the cap counts as 0, as before.
	assert.Equal(t, 0, NewFileLineCount(write("big.txt", string(make([]byte, MaxNewFileStatBytes+1)))))
}
