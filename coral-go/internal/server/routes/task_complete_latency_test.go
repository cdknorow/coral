package routes

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #217: completing a task answered only once its changes.diff was written, and writing it diffs the agent's working tree (git diff,
// and a git diff --no-index per untracked file). On a large tree that took longer than coral-board's 10-second client timeout: every
// completion was reported as failed although it had been committed, and the closed connection cancelled the request's context,
// which killed the git and left the task without its artifact. The completion now answers as soon as it is committed, and the
// artifact is written after the reply, on a context of its own.
func TestCompletingATaskAnswersBeforeItsChangesAreWritten(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "board.db")
	bs, err := boardtest.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	h := NewBoardHandler(bs)
	t.Cleanup(h.WaitBackground)

	release := make(chan struct{})
	var started, finished atomic.Bool
	var ctxErrAtWrite atomic.Value
	coralDir := t.TempDir()
	h.SetTaskArtifactWriter(coralDir, func(ctx context.Context, task *board.Task, path string) error {
		started.Store(true)
		select { // a slow git diff of a large working tree
		case <-release:
		case <-ctx.Done():
		}
		ctxErrAtWrite.Store(errString(ctx.Err()))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		finished.Store(true)
		return os.WriteFile(path, []byte("diff --git a/x b/x\n"), 0o644)
	})

	r := chi.NewRouter()
	r.Post("/api/board/{project}/subscribe", h.Subscribe)
	r.Post("/api/board/{project}/tasks", h.CreateTask)
	r.Post("/api/board/{project}/tasks/claim", h.ClaimTask)
	r.Post("/api/board/{project}/tasks/{taskID}/complete", h.CompleteTaskByID)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	base := srv.URL + "/api/board/p217"

	const sess = "aaaaaaaa-0000-4000-8000-000000000217"
	sessDB, err := storetest.Open(t, filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { sessDB.Close() })
	require.NoError(t, store.NewSessionStore(sessDB).RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sess, AgentType: "claude", AgentName: "worker", WorkingDir: t.TempDir()}))
	h.bs.SetSessionsDB(sessDB.DB)
	registerTaskPlanner(t, h, "p217", "Orchestrator")
	subscribeSession(t, h, "p217", "Worker", sess)
	for _, step := range []struct {
		path string
		body map[string]string
	}{
		{"/tasks", map[string]string{"title": "slow diff", "created_by": "Orchestrator", "assigned_to": "Worker"}},
		{"/tasks/claim", map[string]string{"subscriber_id": "Worker"}},
	} {
		resp := postJSON(t, base+step.path, step.body)
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Contains(t, []int{http.StatusOK, http.StatusCreated}, resp.StatusCode, "%s: %s", step.path, out)
	}

	start := time.Now()
	// A client with a deadline, as coral-board has (10 s): a completion that waits for the slow write fails here instead of hanging.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(base+"/tasks/1/complete", "application/json", strings.NewReader(`{"subscriber_id":"Worker","message":"done"}`))
	require.NoError(t, err, "the completion answers within the client's deadline")
	elapsed := time.Since(start)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "complete: %s", body)
	assert.Less(t, elapsed, 2*time.Second, "the completion answers without waiting for the changes to be written")
	require.Eventually(t, started.Load, 2*time.Second, 10*time.Millisecond, "the changes are still written, after the reply")
	assert.False(t, finished.Load(), "and the reply came while they were still being written")

	// The request is long over and its connection closed: the write must not have been cancelled with it.
	close(release)
	require.Eventually(t, finished.Load, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "", ctxErrAtWrite.Load(), "the write ran on its own context, not the finished request's")
	found, _ := filepath.Glob(filepath.Join(h.coralDir, "artifacts", "tasks", "*", "1", "changes.diff"))
	assert.Len(t, found, 1, "the task's changes.diff is there, under its board")
}

// A task with no agent session still starts no writing at all (#4098), now after the reply too.
func TestNoAfterReplyWriteForATaskWithoutASession(t *testing.T) {
	h := &BoardHandler{}
	var calls atomic.Int32
	h.SetTaskArtifactWriter(t.TempDir(), func(context.Context, *board.Task, string) error { calls.Add(1); return nil })
	empty := ""
	h.persistTaskArtifactAfterReply(nil)
	h.persistTaskArtifactAfterReply(&board.Task{ID: 1})
	h.persistTaskArtifactAfterReply(&board.Task{ID: 2, SessionID: &empty})
	h.WaitBackground()
	assert.Zero(t, calls.Load())
}

// Security's note on #97: two completions of the same task close together (complete then cancel, or a retry) write its
// changes.diff one after the other, never at the same time; different tasks still write in parallel.
func TestAfterReplyWritesOfOneTaskDoNotOverlap(t *testing.T) {
	h := &BoardHandler{}
	var mu sync.Mutex
	inFlight := map[int64]int{}
	maxSame, maxAll, all := 0, 0, 0
	h.SetTaskArtifactWriter(t.TempDir(), func(_ context.Context, task *board.Task, _ string) error {
		mu.Lock()
		inFlight[task.ID]++
		all++
		if inFlight[task.ID] > maxSame {
			maxSame = inFlight[task.ID]
		}
		if all > maxAll {
			maxAll = all
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond) // a diff being written
		mu.Lock()
		inFlight[task.ID]--
		all--
		mu.Unlock()
		return nil
	})
	sid := "aaaaaaaa-0000-4000-8000-000000000097"
	for i := 0; i < 4; i++ {
		h.persistTaskArtifactAfterReply(&board.Task{ID: 1, SessionID: &sid}) // the same task, four times at once
	}
	h.persistTaskArtifactAfterReply(&board.Task{ID: 2, SessionID: &sid})
	h.WaitBackground()
	assert.Equal(t, 1, maxSame, "one task's writes never overlap")
	assert.Equal(t, 2, maxAll, "another task's write runs alongside")
}
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
