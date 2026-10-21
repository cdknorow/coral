package routes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// TestSessionListPerf profiles one /ws/coral tick (buildSessionListForWS)
// against a copy of real data. Skipped unless CORAL_PERF_DIR names a directory
// holding sessions.db, messageboard.db and logs/ (SQLite .backup copies; never
// point it at ~/.coral). Every live session with a log in logs/ is presented
// as a running agent. CORAL_PERF_CPUPROFILE=path writes a CPU profile.
func TestSessionListPerf(t *testing.T) {
	dir := os.Getenv("CORAL_PERF_DIR")
	if dir == "" {
		t.Skip("set CORAL_PERF_DIR to run")
	}
	home, _ := os.UserHomeDir()
	if abs, _ := filepath.Abs(dir); abs == filepath.Join(home, ".coral") {
		t.Fatal("CORAL_PERF_DIR must be a copy, not ~/.coral")
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	ctx := context.Background()

	db, err := store.OpenWithKey(context.Background(), filepath.Join(dir, "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	bs, err := boardtest.Open(filepath.Join(dir, "messageboard.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	logDir := filepath.Join(dir, "logs")
	terminal := newMockTerminal()
	h := NewSessionsHandler(db, &config.Config{LogDir: logDir}, nil, terminal, bs)

	live, err := h.ss.GetAllLiveSessions(ctx)
	require.NoError(t, err)
	// CORAL_PERF_AGENTS=N tops the copy up to N agents with logs by cloning
	// existing ones (live row, log, events, board subscription) under new ids.
	if want, _ := strconv.Atoi(os.Getenv("CORAL_PERF_AGENTS")); want > 0 {
		var withLogs []store.LiveSession
		for _, ls := range live {
			if _, err := os.Stat(naming.LogFile(logDir, ls.AgentType, ls.SessionID)); err == nil && ls.AgentType != "terminal" {
				withLogs = append(withLogs, ls)
			}
		}
		require.NotEmpty(t, withLogs)
		boardRaw, err := sql.Open("sqlite", filepath.Join(dir, "messageboard.db")+"?_pragma=busy_timeout(30000)")
		require.NoError(t, err)
		defer boardRaw.Close()
		cols := "agent_type, agent_name, working_dir, display_name, resume_from_id, flags, is_job, created_at, prompt, board_name, board_server, backend, icon, is_sleeping, board_type, git_diff_mode, capabilities, model, pid, tools, mcp_servers, worktree_path, worktree_repo, team_id, status, context_window, stopped_at, name_color"
		for i := 0; len(withLogs)+i < want; i++ {
			src := withLogs[i%len(withLogs)]
			id := fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i)
			_, err := db.ExecContext(ctx, "INSERT INTO live_sessions (session_id, "+cols+") SELECT ?, "+cols+" FROM live_sessions WHERE session_id = ?", id, src.SessionID)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, "INSERT INTO agent_events (agent_name, session_id, event_type, tool_name, summary, detail_json, created_at, duration_ms, ref_id) SELECT agent_name, ?, event_type, tool_name, summary, detail_json, created_at, duration_ms, ref_id FROM agent_events WHERE session_id = ?", id, src.SessionID)
			require.NoError(t, err)
			data, err := os.ReadFile(naming.LogFile(logDir, src.AgentType, src.SessionID))
			require.NoError(t, err)
			clone := naming.LogFile(logDir, src.AgentType, id)
			require.NoError(t, os.WriteFile(clone, data, 0o644))
			if info, err := os.Stat(naming.LogFile(logDir, src.AgentType, src.SessionID)); err == nil {
				require.NoError(t, os.Chtimes(clone, info.ModTime(), info.ModTime())) // as idle as its source
			}
			_, err = boardRaw.ExecContext(ctx, "INSERT INTO board_subscribers (project, session_id, job_title, webhook_url, origin_server, receive_mode, last_read_id, subscribed_at, is_active, can_peek, subscriber_id, session_name) SELECT project, ?, job_title, webhook_url, origin_server, receive_mode, last_read_id, subscribed_at, is_active, can_peek, subscriber_id || ?, ? FROM board_subscribers WHERE session_name = ? AND is_active = 1", id, fmt.Sprintf(" clone %d", i), naming.SessionName(src.AgentType, id), naming.SessionName(src.AgentType, src.SessionID))
			require.NoError(t, err)
		}
		live, err = h.ss.GetAllLiveSessions(ctx)
		require.NoError(t, err)
	}
	var ids []string
	for _, ls := range live {
		if _, err := os.Stat(naming.LogFile(logDir, ls.AgentType, ls.SessionID)); err == nil {
			terminal.addSession(naming.SessionName(ls.AgentType, ls.SessionID), ls.WorkingDir)
			ids = append(ids, ls.SessionID)
		}
	}
	require.NotEmpty(t, ids)
	req := httptest.NewRequest("GET", "/ws/coral", nil)

	timeIt := func(name string, n int, fn func()) {
		fn() // warm caches
		var d []time.Duration
		for i := 0; i < n; i++ {
			start := time.Now()
			fn()
			d = append(d, time.Since(start))
		}
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		t.Logf("%-34s p50=%-10s max=%s", name, d[len(d)/2].Round(10*time.Microsecond), d[len(d)-1].Round(10*time.Microsecond))
	}

	t.Logf("%d agents with logs, %d live sessions", len(ids), len(live))
	if path := os.Getenv("CORAL_PERF_CPUPROFILE"); path != "" {
		f, err := os.Create(path)
		require.NoError(t, err)
		require.NoError(t, pprof.StartCPUProfile(f))
		for i := 0; i < 20; i++ {
			_, _ = h.buildSessionListForWS(req)
		}
		pprof.StopCPUProfile()
		f.Close()
	}
	timeIt("buildSessionListForWS (full tick)", 30, func() { _, _ = h.buildSessionListForWS(req) })
	if os.Getenv("CORAL_PERF_ITER") != "" {
		if path := os.Getenv("CORAL_PERF_ITER_PROFILE"); path != "" {
			f, err := os.Create(path)
			require.NoError(t, err)
			require.NoError(t, pprof.StartCPUProfile(f))
			defer func() { pprof.StopCPUProfile(); f.Close() }()
		}
		for i := 0; i < 15; i++ {
			start := time.Now()
			_, _ = h.buildSessionListForWS(req)
			t.Logf("iter %2d %s", i, time.Since(start).Round(time.Millisecond))
			time.Sleep(time.Second)
		}
	}
	timeIt("GET /api/sessions/live (List)", 30, func() {
		w := httptest.NewRecorder()
		h.List(w, httptest.NewRequest("GET", "/api/sessions/live", nil))
	})
	// Ten clients ticking together: shared build vs one build each.
	tick := func(build func()) time.Duration {
		start := time.Now()
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); build() }()
		}
		wg.Wait()
		return time.Since(start)
	}
	t.Logf("10 clients, one build each: %s", tick(func() { _, _ = h.buildSessionListForWS(req) }).Round(time.Millisecond))
	h.wsList = wsTickCache{}
	t.Logf("10 clients, shared build:   %s", tick(func() { _, _ = h.sharedWSTick(req) }).Round(time.Millisecond))

	// Entries that differ between two ticks 5s apart (what coral_diff sends).
	if os.Getenv("CORAL_PERF_DIFF") != "" {
		snapshot := func() map[string]string {
			sessions, err := h.buildSessionListForWS(req)
			require.NoError(t, err)
			out := map[string]string{}
			for _, s := range sessions {
				b, _ := json.Marshal(s)
				out[fmt.Sprint(s["session_id"])] = string(b)
			}
			return out
		}
		first := snapshot()
		time.Sleep(5 * time.Second)
		second := snapshot()
		changed := 0
		for k, v := range second {
			if first[k] != v {
				changed++
			}
		}
		t.Logf("coral_diff after 5s: %d of %d entries changed", changed, len(second))
	}
	timeIt("GetAllEditedFileCounts", 10, func() { _, _ = h.ts.GetAllEditedFileCounts(ctx) })
	timeIt("GetEditedFileCounts(live ids)", 10, func() { _, _ = h.ts.GetEditedFileCounts(ctx, ids) })
	timeIt("GetAllUnreadCounts", 10, func() { _, _ = h.bs.GetAllUnreadCounts(ctx) })
	var tmuxNames []string
	for _, ls := range live {
		tmuxNames = append(tmuxNames, naming.SessionName(ls.AgentType, ls.SessionID))
	}
	timeIt("GetUnreadCountsForSessions(live)", 10, func() { _, _ = h.bs.GetUnreadCountsForSessions(ctx, tmuxNames) })
	timeIt("getLogStatus x agents", 10, func() {
		for _, ls := range live {
			getLogStatus(naming.LogFile(logDir, ls.AgentType, ls.SessionID))
		}
	})
	timeIt("discoverAgents", 10, func() { _, _ = h.discoverAgents(req) })
}
