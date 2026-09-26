package store

import (
	"context"
	"fmt"
	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPersonalWorkflowPersistenceAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coral.db")
	db, err := Open(path)
	require.NoError(t, err)
	defer func() { db.Close() }()
	s := NewTaskStore(db)
	ctx := context.Background()
	sid := "one"
	other := "two"
	build, err := s.CreateAgentTaskWithWorkflow(ctx, "agent", "Build", &sid, nil, "", "medium", &board.CreateTaskOpts{Workflow: board.TaskWorkflow{RequiredOutputs: []string{"build"}}})
	require.NoError(t, err)
	child, err := s.CreateAgentTaskWithWorkflow(ctx, "agent", "Test", &sid, nil, "", "medium", &board.CreateTaskOpts{BlockedBy: []board.TaskDep{{TaskID: build.ID, RequiredArtifacts: []string{"build"}}}})
	require.NoError(t, err)
	require.Equal(t, "blocked", child.Status)
	require.Equal(t, AgentTaskBlocked, child.Completed)
	_, err = s.CreateAgentTaskWithWorkflow(ctx, "agent", "Steal", &other, nil, "", "medium", &board.CreateTaskOpts{BlockedBy: []board.TaskDep{{TaskID: build.ID}}})
	require.Error(t, err)
	require.Error(t, s.FinishAgentTask(ctx, child.ID, AgentTaskDone, "bypass"))
	require.Error(t, s.CompleteAgentTaskByTitle(ctx, "agent", "Build", &sid)) // hook cannot bypass required output
	_, err = s.ClaimNextAgentTask(ctx, "agent", &sid, build.ID)
	require.NoError(t, err)
	_, err = s.ClaimNextAgentTask(ctx, "agent", &sid)
	require.Error(t, err)
	evidence := []board.TaskArtifact{{Name: "build", Content: "candidate", Revision: "rev-1"}}
	require.NoError(t, s.FinishAgentTaskWithArtifacts(ctx, build.ID, AgentTaskDone, "built", "success", evidence))
	require.Error(t, s.FinishAgentTask(ctx, build.ID, AgentTaskDone, "overwrite"))
	require.NoError(t, db.Close())
	db, err = Open(path)
	require.NoError(t, err)
	s = NewTaskStore(db)
	ready, err := s.GetAgentTask(ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", ready.Status)
	require.Equal(t, AgentTaskPending, ready.Completed)
	claimed, err := s.ClaimNextAgentTask(ctx, "agent", &sid, child.ID)
	require.NoError(t, err)
	require.Equal(t, evidence, claimed.Workflow.Inputs[0].Artifacts)
	require.NotEmpty(t, claimed.Workflow.Instructions)
}

func TestPersonalWorkflowConcurrentClaims(t *testing.T) {
	db := openTestDB(t)
	s := NewTaskStore(db)
	ctx := context.Background()
	sid := "concurrent"
	for i := 0; i < 10; i++ {
		_, err := s.CreateAgentTask(ctx, "agent", fmt.Sprint(i), &sid, nil)
		require.NoError(t, err)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := s.ClaimNextAgentTask(ctx, "agent", &sid)
			if err == nil && task != nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
}

func TestPersonalWorkflowLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := Open(path)
	require.NoError(t, err)
	// Raw legacy rows simulate an installation before the workflow engine.
	_, err = db.Exec(`INSERT INTO agent_tasks(id,agent_name,session_id,title,body,completed,sort_order,created_at,updated_at,started_at,completed_at,completion_message,cost_usd)
 VALUES(120,'old','session','Historical','Details',1,7,'2025-01-01','2025-01-02','2025-01-01','2025-01-02','Done',1.25),
 (121,'old','session','Active','Preserve',2,8,'2025-01-03','2025-01-03','2025-01-03',NULL,NULL,0)`)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE sqlite_sequence SET seq=200 WHERE name='agent_tasks'")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	db, err = Open(path)
	require.NoError(t, err)
	defer func() { db.Close() }()
	s := NewTaskStore(db)
	task, err := s.GetAgentTask(context.Background(), 120)
	require.NoError(t, err)
	require.Equal(t, "completed", task.Status)
	require.Equal(t, "success", task.Workflow.Outcome)
	require.Equal(t, "Done", *task.CompletionMessage)
	require.Equal(t, 1.25, task.CostUSD)
	require.Equal(t, 7, task.SortOrder)
	active, err := s.GetAgentTask(context.Background(), 121)
	require.NoError(t, err)
	require.Equal(t, "in_progress", active.Status)
	require.Equal(t, "2025-01-03", *active.StartedAt)
	require.NoError(t, db.Close())
	db, err = Open(path)
	require.NoError(t, err)
	var n int
	require.NoError(t, db.Get(&n, "SELECT COUNT(*) FROM board_tasks"))
	require.Equal(t, 2, n)
	fresh, err := NewTaskStore(db).CreateAgentTask(context.Background(), "old", "Fresh", nil, nil)
	require.NoError(t, err)
	require.Greater(t, fresh.ID, int64(200), "migration must not reuse deleted historical task IDs")
}
