package routes

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
)

// QA's #6964: identical diffs are stored once and every task's changes.diff is a hard link to that object (writeChangesArtifact, #3555), so a task whose diff equals an older task's has a file with the OLDER
// task's write time. The older-than-the-task check must not refuse it: this task's own diff, written after the task was made, is served.
func TestTaskChangesDiffSharedWithAnOlderTaskIsStillServed(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	registerTaskPlanner(t, handler, "eval", "Orchestrator")
	coralDir := t.TempDir()
	handler.SetTaskArtifactWriter(coralDir, func(context.Context, *board.Task, string) error { return nil })
	base := server.URL + "/api/board/eval/tasks"
	resp := postJSON(t, base, map[string]string{"title": "Evaluate", "created_by": "Orchestrator"})
	resp.Body.Close()
	tasks, err := handler.bs.ListTasks(context.Background(), "eval")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	task := tasks[0]
	diff := []byte("diff --git a/a.txt b/a.txt\n+an unchanged checkout\n")

	// an older task's diff of the same content: stored first, long ago (its inode is the shared object)
	older := filepath.Join(coralDir, "artifacts", "tasks", "0123456789abcdef", "99", "changes.diff")
	require.NoError(t, writeChangesArtifact(older, diff))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(older, old, old))
	// this task's own diff, written after the task was made: the same bytes, so a hard link to the same object
	own := handler.taskArtifactPath(task.BoardID, task.ID)
	require.NoError(t, writeChangesArtifact(own, diff))
	a, err := os.Stat(older)
	require.NoError(t, err)
	b, err := os.Stat(own)
	require.NoError(t, err)
	require.True(t, os.SameFile(a, b), "the two diffs are one stored object")

	r, err := http.Get(base + "/" + strconv.FormatInt(task.ID, 10) + "/changes.diff")
	require.NoError(t, err)
	r.Body.Close()
	assert.Equal(t, http.StatusOK, r.StatusCode, "this task's own diff, written after the task, is served although it shares an older object")
}
