//go:build !windows

package background

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3730: a pass runs 'git status' once per repository (it ran twice: for the dirty count and
// for the changed files), and both still come from it. A git on PATH that logs each run
// counts them. gitutil.Command's filter guard ('git config --get-regexp') runs right before
// each git that can run a filter driver, and only those (#3847: it ran before every command).
func TestGitPollerRunsStatusOncePerPass(t *testing.T) {
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "old.txt"), []byte("x\n"), 0o644))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "base")
	runGit(t, repo, "checkout", "-q", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644))
	runGit(t, repo, "mv", "old.txt", "new.txt")
	time.Sleep(1100 * time.Millisecond) // sleep-ok: the new file must be newer than the base commit's second
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("u\n"), 0o644))

	shim := t.TempDir()
	logPath := filepath.Join(shim, "runs.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + logPath + "'\nexec '" + realGit + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755))
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	gs := store.NewGitStore(db)
	p := NewGitPoller(gs, nil, time.Minute)
	sid := "s-1"
	p.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "a", AgentType: "claude", SessionID: sid, WorkingDirectory: repo}}, nil
	})
	ctx := context.Background()
	require.NoError(t, p.PollOnce(ctx))

	raw, err := os.ReadFile(logPath)
	require.NoError(t, err)
	runs := strings.Split(strings.TrimSpace(string(raw)), "\n")
	status, guards, filterCapable := 0, 0, 0
	for _, r := range runs {
		if strings.Contains(r, " status ") {
			status++
		}
		if strings.Contains(r, " status ") || strings.Contains(r, " diff ") {
			filterCapable++
		}
		if strings.Contains(r, "--get-regexp") {
			guards++
		}
	}
	t.Logf("git runs in one pass for one agent: %d (%d of them the filter guard), status: %d", len(runs), guards, status)
	assert.Equal(t, 1, status, "one status per pass")
	assert.Equal(t, filterCapable, guards, "the filter guard runs before each status and diff, and no other git")

	snap, err := gs.GetLatestGitStateBySession(ctx, sid)
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.Equal(t, 3, snap.DirtyCount, "a.txt modified, old.txt renamed to new.txt, untracked.txt")
	files, found, err := gs.GetChangedFiles(ctx, "", &sid, "branch_point")
	require.NoError(t, err)
	require.True(t, found)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Filepath+":"+f.Status)
	}
	assert.ElementsMatch(t, []string{"a.txt:M", "new.txt:R", "untracked.txt:??"}, paths)
}
