//go:build windows

package background

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A git killed halfway through `worktree add` may still hold a file of the half-made checkout for a moment on Windows, so the rollback's first RemoveAll fails (#6927). It tries once more after 300 ms. This holds a
// file open inside a half-made run folder (Go opens a file without FILE_SHARE_DELETE, as git's handles do: it cannot be deleted while open), releases it after about 100 ms, and expects no folder left.
func TestRollbackRemovesAHalfMadeFolderWhoseFileIsHeldForAMoment(t *testing.T) {
	e := newExitEnv(t, "exit-rollback-held")
	repo := exitGitRepo(t)
	dir := e.s.runWorktreeDir(repo, e.runID)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	held := filepath.Join(dir, "sub", "held.txt")
	f, err := os.OpenFile(held, os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	require.Error(t, os.Remove(held), "the file cannot be deleted while it is held open: the first RemoveAll must fail")

	go func() {
		time.Sleep(100 * time.Millisecond)
		f.Close()
	}()
	start := time.Now()
	e.s.rollBackWorktreeAdd(repo, dir, "coral/job-run-held", e.runID)

	assert.NoDirExists(t, dir, "the retried RemoveAll leaves no folder")
	assert.GreaterOrEqual(t, time.Since(start), 90*time.Millisecond, "it waited for the file to be let go")
}
