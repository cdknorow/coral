package routes

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/internal/terminalinput"
)

// TestBoardStatusLoad measures GET /api/board/{project}/status latency under
// concurrent polling, with and without a board-notifier pass and background
// board writes running. It is opt-in because it seeds a large history:
//
//	CORAL_LOADTEST=1 go test ./internal/server/routes/ -run TestBoardStatusLoad -v -count=1
//
// Knobs (env): CORAL_LOADTEST_TASKS (completed tasks per board, default 1500),
// CORAL_LOADTEST_MESSAGES (messages per board, default 10000),
// CORAL_LOADTEST_CONCURRENCY (simultaneous requests per burst, default 8),
// CORAL_LOADTEST_ROUNDS (bursts per phase, default 10),
// CORAL_LOADTEST_MIN_PHASE_S (minimum seconds per loaded phase, default 10),
// CORAL_LOADTEST_ASSERT_P95_MS (fail when the loaded p95 exceeds it; unset = report only).
//
// Everything lives in t.TempDir(); CORAL_DATA_DIR is pointed there too, so
// the test never touches ~/.coral.
func TestBoardStatusLoad(t *testing.T) {
	if os.Getenv("CORAL_LOADTEST") != "1" {
		t.Skip("set CORAL_LOADTEST=1 to run the /status load test")
	}
	tasksPerBoard := loadTestEnvInt("CORAL_LOADTEST_TASKS", 1500)
	messagesPerBoard := loadTestEnvInt("CORAL_LOADTEST_MESSAGES", 10000)
	concurrency := loadTestEnvInt("CORAL_LOADTEST_CONCURRENCY", 8)
	rounds := loadTestEnvInt("CORAL_LOADTEST_ROUNDS", 10)
	minPhase := time.Duration(loadTestEnvInt("CORAL_LOADTEST_MIN_PHASE_S", 10)) * time.Second

	dataDir := t.TempDir()
	t.Setenv("CORAL_DATA_DIR", dataDir)
	// The notifier logs every subscriber on every pass; keep output to the report.
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })
	ctx := context.Background()

	db, err := storetest.Open(t, filepath.Join(dataDir, "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	boardPath := filepath.Join(dataDir, "messageboard.db")
	bs, err := boardtest.Open(boardPath)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	terminal := newMockTerminal()
	h := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, terminal, bs)
	ss := store.NewSessionStore(db)

	// Seed: 3 boards, 20 subscribers (7/7/6), each a live claude session.
	boards := []string{"load-alpha", "load-beta", "load-gamma"}
	var agents []background.AgentInfo
	for i := 0; i < 20; i++ {
		team := boards[i%len(boards)]
		sid := uuid.NewString()
		name := fmt.Sprintf("Agent %02d", i)
		title := "Developer"
		mode := "mentions"
		if i < len(boards) {
			name, title, mode = "Orchestrator", "Orchestrator", "all"
		}
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: name, AgentType: "claude", BoardName: &team, WorkingDir: "/tmp", DisplayName: &name}))
		_, err := bs.Subscribe(ctx, team, name, title, "claude-"+sid, nil, mode)
		require.NoError(t, err)
		terminal.addSession("claude-"+sid, "/tmp")
		_, err = h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: name, SessionID: &sid, EventType: "stop"})
		require.NoError(t, err)
		agents = append(agents, background.AgentInfo{AgentName: name, AgentType: "claude", SessionID: sid, DisplayName: name})
	}

	// Open tasks through the store API (assigned + pool), history via raw SQL.
	for _, team := range boards {
		for i := 0; i < 10; i++ {
			assignee := []string{}
			if i%2 == 0 {
				assignee = append(assignee, fmt.Sprintf("Agent %02d", 3+i))
			}
			_, err := bs.CreateTask(ctx, team, fmt.Sprintf("Open task %d", i), "body", "medium", "Orchestrator", assignee...)
			require.NoError(t, err)
		}
	}
	raw, err := sql.Open("sqlite", boardPath+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })
	seedStart := time.Now()
	tx, err := raw.Begin()
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, team := range boards {
		for i := 0; i < tasksPerBoard; i++ {
			_, err := tx.Exec(`INSERT INTO board_tasks (board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at)
				VALUES (?, ?, ?, 'completed', 'medium', 'Orchestrator', 'Agent 05', 'Agent 05', 'done', ?, ?, ?)`, team, fmt.Sprintf("Historical task %d", i), "historical body", now, now, now)
			require.NoError(t, err)
		}
		for i := 0; i < messagesPerBoard; i++ {
			_, err := tx.Exec(`INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at) VALUES (?, ?, ?, ?, ?)`,
				team, "Agent 07", "Agent 07", fmt.Sprintf("history message %d @Agent %02d progress update with some body text", i, i%20), now)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tx.Commit())
	t.Logf("seeded 3 boards: 20 subscribers, %d completed + 10 open tasks/board, %d messages/board in %s", tasksPerBoard, messagesPerBoard, time.Since(seedStart).Round(time.Millisecond))

	r := chi.NewRouter()
	r.Get("/api/board/{project}/status", h.TeamAvailability)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: concurrency * 2}}
	target := srv.URL + "/api/board/" + boards[0] + "/status"

	get := func() (time.Duration, error) {
		start := time.Now()
		resp, err := client.Get(target)
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("status %d", resp.StatusCode)
		}
		return time.Since(start), nil
	}
	burst := func() []time.Duration {
		var wg sync.WaitGroup
		out := make([]time.Duration, concurrency)
		errs := make([]error, concurrency)
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				out[i], errs[i] = get()
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}
		return out
	}

	// Phase 1: single requests, nothing else running.
	var serial []time.Duration
	for i := 0; i < rounds; i++ {
		d, err := get()
		require.NoError(t, err)
		serial = append(serial, d)
	}
	reportLatency(t, "serial (idle)", serial)

	// Phase 2: concurrent bursts, nothing else running.
	var idle []time.Duration
	for i := 0; i < rounds; i++ {
		idle = append(idle, burst()...)
	}
	reportLatency(t, fmt.Sprintf("burst x%d (idle)", concurrency), idle)

	// One notifier pass on its own, for scale.
	notifier := background.NewBoardNotifier(bs, &loadTestRuntime{}, time.Hour)
	notifier.SetDiscoverFn(func(context.Context) ([]background.AgentInfo, error) { return agents, nil })
	passStart := time.Now()
	require.NoError(t, notifier.RunOnce(ctx))
	t.Logf("single notifier pass (20 subscribers): %s", time.Since(passStart).Round(time.Millisecond))

	// Phases 3 and 4: concurrent bursts while the notifier passes back to back
	// (in production NotifyNow fires a pass on every board post, so a busy
	// board sees near-continuous passes), then also with agents posting and
	// reading.
	loadedPhase := func(label string, withWrites bool) time.Duration {
		bgCtx, stop := context.WithCancel(ctx)
		var passes, writes atomic.Int64
		var bg sync.WaitGroup
		bg.Add(1)
		go func() {
			defer bg.Done()
			for bgCtx.Err() == nil {
				_ = notifier.RunOnce(bgCtx)
				passes.Add(1)
			}
		}()
		if withWrites {
			bg.Add(1)
			go func() {
				defer bg.Done()
				for i := 0; bgCtx.Err() == nil; i++ {
					team := boards[i%len(boards)]
					_, _ = bs.PostMessage(bgCtx, team, "Agent 08", fmt.Sprintf("load write %d @Orchestrator", i), nil)
					_, _ = bs.ReadMessages(bgCtx, team, "Orchestrator", 50)
					writes.Add(1)
					time.Sleep(20 * time.Millisecond)
				}
			}()
		}
		// Keep bursting for a minimum time so a fast /status still overlaps
		// many notifier passes and writes.
		var loaded []time.Duration
		phaseStart := time.Now()
		for i := 0; i < rounds || time.Since(phaseStart) < minPhase; i++ {
			loaded = append(loaded, burst()...)
		}
		stop()
		bg.Wait()
		p95 := reportLatency(t, fmt.Sprintf("burst x%d (%s)", concurrency, label), loaded)
		t.Logf("  background: %d notifier passes, %d post+read cycles", passes.Load(), writes.Load())
		return p95
	}
	loadedPhase("notifier", false)
	p95 := loadedPhase("notifier + writes", true)

	if limit := loadTestEnvInt("CORAL_LOADTEST_ASSERT_P95_MS", 0); limit > 0 {
		require.LessOrEqual(t, p95, time.Duration(limit)*time.Millisecond, "loaded /status p95 exceeds CORAL_LOADTEST_ASSERT_P95_MS")
	}
}

type loadTestRuntime struct{}

func (*loadTestRuntime) SpawnAgent(context.Context, string, string, string, string) error { return nil }
func (*loadTestRuntime) SendInput(context.Context, terminalinput.Origin, string, string) error {
	return nil
}
func (*loadTestRuntime) KillAgent(context.Context, string) error                    { return nil }
func (*loadTestRuntime) IsAlive(context.Context, string) bool                       { return true }
func (*loadTestRuntime) ListAgents(context.Context) ([]background.AgentInfo, error) { return nil, nil }

func reportLatency(t *testing.T, label string, ds []time.Duration) time.Duration {
	t.Helper()
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	pct := func(p float64) time.Duration { return sorted[int(p*float64(len(sorted)-1)+0.5)] }
	p95 := pct(0.95)
	t.Logf("%-34s n=%-4d p50=%-8s p95=%-8s max=%s", label, len(sorted), pct(0.50).Round(time.Millisecond), p95.Round(time.Millisecond), sorted[len(sorted)-1].Round(time.Millisecond))
	return p95
}

func loadTestEnvInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
