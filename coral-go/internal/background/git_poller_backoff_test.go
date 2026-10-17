package background

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
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
	require.Equal(t, 1, strings.Count(logged.String(), "level=WARN"), logged.String())
	require.Empty(t, p.failing)
}
