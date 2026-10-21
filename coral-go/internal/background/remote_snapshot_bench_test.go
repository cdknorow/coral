package background

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// loadedHub returns a hub holding k online remotes with n ~1 KB sessions each,
// without running any pollers.
func loadedHub(k, n int) *RemoteAgentHub {
	h := NewRemoteAgentHub(newFakeRegistry(), nil, RemoteAgentHubConfig{OfflineGrace: -1}) // each failure at once (#3649)
	for r := 0; r < k; r++ {
		id := fmt.Sprintf("r%02d", r)
		st := &remoteState{srv: store.RemoteServer{ID: id, Label: id, URL: "http://" + id}, status: store.RemoteStatusOnline}
		for i := 0; i < n; i++ {
			st.sessions = append(st.sessions, benchSession(id, i))
		}
		h.remotes[id] = st
	}
	return h
}

func benchSession(id string, i int) map[string]any {
	sid := fmt.Sprintf("%s-%04d-0000-4000-8000-%012d", id, i, i)
	return map[string]any{
		"agent_type": "claude", "awaiting_user": false, "board_is_orchestrator": i == 0, "board_job_title": fmt.Sprintf("Agent %02d", i),
		"board_project": "scale-team", "board_unread": i % 3, "branch": "feature/scale-test", "changed_file_count": i % 7,
		"commands": map[string]any{"clear": "/clear", "compress": "/compact"}, "context_pct": 42, "context_window": 200000,
		"display_name": fmt.Sprintf("Agent %02d", i), "done": false, "first_prompt": "Implement the next task from the board and report back",
		"git_state": map[string]any{"ahead": 1, "behind": 0, "dirty": 3, "is_worktree": true, "base_branch": "main"}, "log_path": "/tmp/claude_coral_" + sid + ".log",
		"name": "work", "repo_name": "coral", "session_id": sid, "sleeping": false, "staleness_seconds": 3, "status": "Running tests",
		"summary": "Working on the scale test harness", "tmux_session": "claude-" + sid, "working": true, "working_directory": "/home/dev/src/coral",
	}
}

func BenchmarkRemoteSnapshot(b *testing.B) {
	for _, kn := range [][2]int{{5, 50}, {20, 20}} {
		h := loadedHub(kn[0], kn[1])
		b.Run(fmt.Sprintf("K=%d_N=%d", kn[0], kn[1]), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				h.Snapshot()
			}
		})
	}
}

// Snapshot is built once and shared until something it shows changes:
// sessions, status (the stale flag) or anything a notify reports.
func TestSnapshotCachedUntilChange(t *testing.T) {
	reg := newFakeRegistry()
	reg.put("r00", "http://r00")
	h := loadedHub(1, 2)
	h.reg = reg
	ctx := context.Background()

	a, b := h.Snapshot(), h.Snapshot()
	require.Same(t, &a.Sessions[0], &b.Sessions[0], "unchanged state reuses the snapshot")
	require.Same(t, &a.SessionsJSON()[0], &b.SessionsJSON()[0], "and its encoding")

	// New session data via the poll path.
	h.recordOnline(ctx, "r00", []map[string]any{benchSession("r00", 9)})
	c := h.Snapshot()
	require.Len(t, c.Sessions, 1)
	require.Equal(t, "r00-0009-0000-4000-8000-000000000009", c.Sessions[0]["session_id"])
	require.Contains(t, string(c.SessionsJSON()[0]), "000000000009")

	// Status change marks the sessions stale.
	h.recordFailure(ctx, "r00", errors.New("down"))
	d := h.Snapshot()
	require.Equal(t, true, d.Sessions[0]["stale"])
	require.Equal(t, store.RemoteStatusUnreachable, d.Servers[0].Status)
	require.Equal(t, false, c.Sessions[0]["stale"] == true, "an earlier snapshot is not modified")

	// Any notify drops the cache.
	h.notify()
	e := h.Snapshot()
	require.NotSame(t, &d.Sessions[0], &e.Sessions[0])
}
