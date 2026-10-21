package routes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store/storetest"
)

type staticRemoteSource struct{ snap background.RemoteSnapshot }

func (s staticRemoteSource) Snapshot() background.RemoteSnapshot { return s.snap }
func (s staticRemoteSource) Subscribe() (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}

func benchRemoteSnapshot(k, n int) background.RemoteSnapshot {
	var servers []background.RemoteServerStatus
	var sessions []map[string]any
	for r := 0; r < k; r++ {
		id := fmt.Sprintf("r%02d", r)
		servers = append(servers, background.RemoteServerStatus{ID: id, Label: id, Status: "online", Agents: n})
		for i := 0; i < n; i++ {
			sid := fmt.Sprintf("%s-%04d-0000-4000-8000-%012d", id, i, i)
			sessions = append(sessions, map[string]any{
				"agent_type": "claude", "awaiting_user": false, "board_job_title": fmt.Sprintf("Agent %02d", i), "board_project": "scale-team",
				"branch": "feature/x", "commands": map[string]any{"clear": "/clear", "compress": "/compact"}, "context_pct": 42, "context_window": 200000,
				"display_name": fmt.Sprintf("Agent %02d <dev>", i), "first_prompt": "Implement the next task & report", "git_state": map[string]any{"ahead": 1, "dirty": 3},
				"log_path": "/tmp/claude_coral_" + sid + ".log", "name": "work", "session_id": sid, "status": "Running tests", "summary": "Working on it",
				"tmux_session": "claude-" + sid, "working": true, "working_directory": "/home/dev/src/coral", "server": id,
			})
		}
	}
	return background.NewRemoteSnapshot(servers, sessions)
}

// BenchmarkMergedRemoteList is GET /api/sessions/live on a hub with no local
// agents and k remotes of n agents: the merge and encoding cost per call.
func BenchmarkMergedRemoteList(b *testing.B) {
	for _, kn := range [][2]int{{5, 50}, {20, 20}} {
		b.Run(fmt.Sprintf("K=%d_N=%d", kn[0], kn[1]), func(b *testing.B) {
			db, err := storetest.Open(b, b.TempDir()+"/test.db")
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { db.Close() })
			h := NewSessionsHandler(db, &config.Config{LogDir: b.TempDir()}, nil, newMockTerminal(), nil)
			h.SetRemoteAgents(staticRemoteSource{benchRemoteSnapshot(kn[0], kn[1])})
			req := httptest.NewRequest("GET", "/api/sessions/live", nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w := httptest.NewRecorder()
				h.List(w, req)
			}
		})
	}
}

// The hub list built from the snapshot's pre-encoded sessions is byte-for-byte
// what encoding the session maps produced, including characters the encoder
// escapes (<, >, &, U+2028) and non-ASCII text.
func TestMergedRemoteListBytesUnchanged(t *testing.T) {
	db, err := storetest.Open(t, t.TempDir()+"/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, newMockTerminal(), nil)
	snap := benchRemoteSnapshot(3, 4)
	snap.Sessions[0]["summary"] = "a <b> & c   é 日本 \"quoted\" \\ back"
	snap.Sessions[1]["nested"] = map[string]any{"z": 1, "a": []any{nil, true, 1.5, "x"}}
	snap = background.NewRemoteSnapshot(snap.Servers, snap.Sessions)
	h.SetRemoteAgents(staticRemoteSource{snap})

	w := httptest.NewRecorder()
	h.List(w, httptest.NewRequest("GET", "/api/sessions/live", nil))
	require.Equal(t, 200, w.Code)

	var want bytes.Buffer
	require.NoError(t, json.NewEncoder(&want).Encode(map[string]any{
		"sessions": snap.Sessions, // the previous output: the maps themselves
		"servers":  snap.Servers,
	}))
	require.Equal(t, want.String(), w.Body.String())
}
