package background

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// What a git poll keeps bounded (#4599 second ranking, item 8, Backend's SHOULD): a repository's changed files are
// capped at maxChangedFiles, and a failing folder nobody works in any more is forgotten.

func newPollerFor(t *testing.T, dir, sid string) (*GitPoller, *store.GitStore) {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	gs := store.NewGitStore(db)
	p := NewGitPoller(gs, nil, time.Minute)
	p.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "a", AgentType: "claude", SessionID: sid, WorkingDirectory: dir}}, nil
	})
	return p, gs
}

func TestGitPollCapsTheChangedFiles(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	runGit(t, dir, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.txt"), []byte("x\n"), 0o644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "base")
	for i := 0; i <= maxChangedFiles; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("new-%04d.txt", i)), []byte("y\n"), 0o644))
	}
	sid := "s-cap"
	p, gs := newPollerFor(t, dir, sid)
	require.NoError(t, p.PollOnce(context.Background()))
	files, found, err := gs.GetChangedFiles(context.Background(), "", &sid, "branch_point")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, files, maxChangedFiles, "capped, not all %d", maxChangedFiles+1)
}

func TestGitPollForgetsFailingFoldersNobodyWorksIn(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	p, _ := newPollerFor(t, dir, "s-forget")
	gone := filepath.Join(t.TempDir(), "gone")
	p.failing[gone] = p.now().Add(time.Hour) // failed before; its agent has left
	require.NoError(t, p.PollOnce(context.Background()))
	_, still := p.failing[gone]
	require.False(t, still, "a folder that is no longer anyone's working directory is forgotten")
	_, current := p.failing[dir]
	require.True(t, current, "the current folder (not a repository) is backed off, not forgotten")
}
