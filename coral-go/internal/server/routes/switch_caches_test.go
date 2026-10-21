package routes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// gitRepoWith makes a git repository with n tracked files.
func gitRepoWith(t testing.TB, n int) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("pkg%d", i%50), fmt.Sprintf("f%d.go", i))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("package x\n"), 0o644))
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@example.test", "-c", "user.name=t", "commit", "-qm", "x"}} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	return dir
}

// #3721 (5): the file list is listed once per repository however many ask at once, served from
// the cache afterwards, and listed again (equal to a fresh listing) after the git poller signals
// a change in the folder or a subfolder, or after the TTL. A failed listing isn't kept.
func TestFileListCacheListsOnceAndFollowsChanges(t *testing.T) {
	repo := gitRepoWith(t, 30)
	c := newFileListCache(time.Hour)
	var calls atomic.Int32
	release := make(chan struct{})
	c.list = func(ctx context.Context, w string) ([]string, error) {
		calls.Add(1)
		<-release
		return gitListFiles(ctx, w)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.get(context.Background(), repo) }()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.EqualValues(t, 1, calls.Load(), "8 callers at once: one listing")
	files, err := c.get(context.Background(), repo)
	require.NoError(t, err)
	assert.Len(t, files, 30)
	assert.EqualValues(t, 1, calls.Load(), "then from the cache")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "new.go"), []byte("package x\n"), 0o644))
	c.invalidate(filepath.Join(repo, "pkg3")) // the poller saw a change in an agent's subfolder
	files, err = c.get(context.Background(), repo)
	require.NoError(t, err)
	fresh, err := gitListFiles(context.Background(), repo)
	require.NoError(t, err)
	assert.Equal(t, fresh, files, "after a change: equal to a fresh listing")
	assert.Contains(t, files, "new.go")

	now := time.Now()
	c.now = func() time.Time { return now.Add(2 * time.Hour) }
	_, _ = c.get(context.Background(), repo)
	assert.EqualValues(t, 3, calls.Load(), "after the TTL: listed again")

	c.list = func(context.Context, string) ([]string, error) { return nil, errors.New("git failed") }
	_, err = c.get(context.Background(), "/no/such/repo")
	assert.Error(t, err)
	c.mu.Lock()
	_, kept := c.entries["/no/such/repo"]
	c.mu.Unlock()
	assert.False(t, kept, "a failure isn't kept")
}

// The poller's signal reaches the server's cache (OnFilesChanged, registered at init).
func TestThePollersSignalDropsTheServersFileList(t *testing.T) {
	repo := gitRepoWith(t, 3)
	_, err := fileLists.get(context.Background(), repo)
	require.NoError(t, err)
	background.NotifyFilesChanged(repo)
	fileLists.mu.Lock()
	_, kept := fileLists.entries[repo]
	fileLists.mu.Unlock()
	assert.False(t, kept)
}

// #3721 (6): a board's status is computed once however many ask at once, served for the TTL,
// and computed again (the fresh reply) afterwards; only a 200 is kept; a request for activity is
// never served from it.
func TestReplyCacheComputesOnceAndKeepsOnlyOK(t *testing.T) {
	c := newReplyCache(time.Hour)
	var computed atomic.Int32
	release := make(chan struct{})
	reply := func(body string) func(http.ResponseWriter) {
		return func(w http.ResponseWriter) {
			computed.Add(1)
			<-release
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
	}
	var wg sync.WaitGroup
	bodies := make([]string, 8)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			c.serve(rec, httptest.NewRequest("GET", "/api/board/team/status", nil), "team", reply(`{"v":1}`))
			bodies[i] = rec.Body.String()
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.EqualValues(t, 1, computed.Load())
	for _, b := range bodies {
		assert.Equal(t, `{"v":1}`, b)
	}

	rec := httptest.NewRecorder()
	c.serve(rec, httptest.NewRequest("GET", "/", nil), "team", reply(`{"v":2}`))
	assert.Equal(t, `{"v":1}`, rec.Body.String(), "within the TTL: the kept reply")
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	now := time.Now()
	c.now = func() time.Time { return now.Add(2 * time.Hour) }
	rec = httptest.NewRecorder()
	c.serve(rec, httptest.NewRequest("GET", "/", nil), "team", reply(`{"v":2}`))
	assert.Equal(t, `{"v":2}`, rec.Body.String(), "after the TTL: the fresh reply")

	fail := func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }
	rec = httptest.NewRecorder()
	c.serve(rec, httptest.NewRequest("GET", "/", nil), "other", fail)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	c.mu.Lock()
	_, kept := c.entries["other"]
	c.mu.Unlock()
	assert.False(t, kept, "an error isn't kept")

	assert.True(t, boardStatusCacheable(httptest.NewRequest("GET", "/api/board/t/status", nil)))
	assert.False(t, boardStatusCacheable(httptest.NewRequest("GET", "/api/board/t/status?activity=1&subscriber_id=Orchestrator", nil)), "activity is the caller's own")
	assert.False(t, boardStatusCacheable(httptest.NewRequest("GET", "/api/board/t/status?activity=true", nil)))
}

// BenchmarkSearchFilesList: a 5000-file repository listed by git (as every switch did) and from
// the cache.
func BenchmarkSearchFilesList(b *testing.B) {
	repo := gitRepoWith(b, 5000)
	b.Run("git", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = gitListFiles(context.Background(), repo)
		}
	})
	c := newFileListCache(time.Hour)
	_, _ = c.get(context.Background(), repo)
	b.Run("cached", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = c.get(context.Background(), repo)
		}
	})
}

// BenchmarkBoardStatus: a board's status with 24 live agents, computed and from the cache.
func BenchmarkBoardStatus(b *testing.B) {
	db, err := storetest.Open(b, filepath.Join(b.TempDir(), "s.db"))
	require.NoError(b, err)
	defer db.Close()
	bs, err := boardtest.Open(filepath.Join(b.TempDir(), "board.db"))
	require.NoError(b, err)
	defer bs.Close()
	h := NewSessionsHandler(db, &config.Config{LogDir: b.TempDir()}, nil, newMockTerminal(), bs)
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	for i := 0; i < 24; i++ {
		sid := fmt.Sprintf("aaaaaaaa-3721-4000-8000-%012d", i)
		require.NoError(b, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: fmt.Sprintf("a%d", i), AgentType: "claude", WorkingDir: b.TempDir()}))
		_, err := bs.Subscribe(ctx, "team", fmt.Sprintf("Agent %d", i), "Dev", "claude-"+sid, nil, "")
		require.NoError(b, err)
	}
	req := func() *http.Request {
		r := httptest.NewRequest("GET", "/api/board/team/status", nil)
		return withURLParam(r, "project", "team")
	}
	b.Run("computed", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			h.teamAvailabilityUncached(httptest.NewRecorder(), req())
		}
	})
	b.Run("8-at-once-separately", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var wg sync.WaitGroup
			for k := 0; k < 8; k++ {
				wg.Add(1)
				go func() { defer wg.Done(); h.teamAvailabilityUncached(httptest.NewRecorder(), req()) }()
			}
			wg.Wait()
		}
	})
	b.Run("8-at-once-shared", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var wg sync.WaitGroup
			for k := 0; k < 8; k++ {
				wg.Add(1)
				go func() { defer wg.Done(); h.TeamAvailability(httptest.NewRecorder(), req()) }()
			}
			wg.Wait()
		}
	})
}

// A status read after a change is the fresh one: the board's cache keeps nothing (ttl 0); only
// requests in flight together share a computation.
func TestBoardStatusIsNeverStale(t *testing.T) {
	c := newReplyCache(0)
	n := 0
	compute := func(w http.ResponseWriter) { n++; fmt.Fprintf(w, `{"n":%d}`, n) }
	for want := 1; want <= 3; want++ {
		rec := httptest.NewRecorder()
		c.serve(rec, httptest.NewRequest("GET", "/", nil), "team", compute)
		assert.Equal(t, fmt.Sprintf(`{"n":%d}`, want), rec.Body.String())
	}
}

func withURLParam(r *http.Request, key, value string) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
}
