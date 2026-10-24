package background

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #4098 (the #3995 soak: a warning every 2 minutes for each working folder that isn't a git
// repository): such a folder is warned about once, then left alone for gitFailureBackoff;
// checked again after that, it is picked up once it has become a repository.
func TestGitPollerWarnsOnceForAFolderThatIsNotARepository(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir()) // git reports the real path (macOS: /private/var)
	require.NoError(t, err)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir)) // never find a repository above the test folder
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	gs := store.NewGitStore(db)
	p := NewGitPoller(gs, nil, time.Minute)
	var logged bytes.Buffer
	p.logger = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo}))
	clock := time.Now()
	p.now = func() time.Time { return clock }
	sid := "s-plain"
	p.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "a", AgentType: "claude", SessionID: sid, WorkingDirectory: dir}}, nil
	})
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, p.PollOnce(ctx))
		clock = clock.Add(2 * time.Minute)
	}
	require.Equal(t, 1, strings.Count(logged.String(), "git query failed"), logged.String())

	// It becomes a repository; it is checked again only after the backoff.
	runGit(t, dir, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "base")
	require.NoError(t, p.PollOnce(ctx))
	require.NotContains(t, logged.String(), "git query works again", "still within the backoff")
	clock = clock.Add(gitFailureBackoff)
	require.NoError(t, p.PollOnce(ctx))
	require.Contains(t, logged.String(), "git query works again")
	// The warnings this test is about: one for the folder, however long the whole run took. (Not every WARN line: a poll that takes over 5 s on a slow runner logs "git poll slow", a warning of its own, #6867.)
	require.Equal(t, 1, strings.Count(logged.String(), "git query failed"), logged.String())
	require.Empty(t, p.failing)
}

// #6867: with a git that is slow AND fails (a loaded Windows runner), a folder that is not a repository is still warned about once: the first failed query marks it failing, however many polls follow and however
// long each takes. The slowness shows as "git poll slow", a different line.
func TestGitPollerWarnsOnceWithASlowFailingGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a shell script")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nsleep 0.4\nexit 128\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	p := NewGitPoller(store.NewGitStore(db), nil, time.Minute)
	var logged bytes.Buffer
	p.logger = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo}))
	clock := time.Now()
	p.now = func() time.Time { return clock }
	p.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "a", AgentType: "claude", SessionID: "s-slow", WorkingDirectory: dir}, {AgentName: "b", AgentType: "claude", SessionID: "s-slow-2", WorkingDirectory: dir}}, nil
	})
	for i := 0; i < 4; i++ {
		require.NoError(t, p.PollOnce(context.Background()))
		clock = clock.Add(2 * time.Minute)
	}
	assert.Equal(t, 1, strings.Count(logged.String(), "git query failed"), "one warning for the folder: %s", logged.String())
}
