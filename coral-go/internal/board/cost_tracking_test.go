package board

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// testStoreWithSessionsDB creates a board store with a wired-up sessions DB
// containing live_sessions and proxy_requests tables.
func testStoreWithSessionsDB(t *testing.T) (*Store, *sqlx.DB) {
	t.Helper()

	// Board DB
	boardDBPath := filepath.Join(t.TempDir(), "board_test.db")
	s, err := NewStoreWithKey(boardDBPath, "")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	// Sessions DB (separate file, like production)
	sessDBPath := filepath.Join(t.TempDir(), "sessions_test.db")
	sessDB, err := sqlx.Open("sqlite", sessDBPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	require.NoError(t, err)
	sessDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sessDB.Close() })

	// Create tables the board store expects to JOIN against.
	sessDB.MustExec(`CREATE TABLE IF NOT EXISTS live_sessions (
		session_id   TEXT PRIMARY KEY,
		agent_type   TEXT NOT NULL DEFAULT '',
		agent_name   TEXT NOT NULL DEFAULT '',
		working_dir  TEXT NOT NULL DEFAULT '',
		display_name TEXT,
		board_name   TEXT,
		status       TEXT NOT NULL DEFAULT 'active',
		created_at   TEXT NOT NULL DEFAULT ''
	)`)
	sessDB.MustExec(`CREATE TABLE IF NOT EXISTS proxy_requests (
		id                  INTEGER PRIMARY KEY AUTOINCREMENT,
		request_id          TEXT NOT NULL UNIQUE,
		session_id          TEXT NOT NULL,
		agent_name          TEXT,
		agent_type          TEXT,
		board_name          TEXT,
		provider            TEXT NOT NULL DEFAULT '',
		model_requested     TEXT NOT NULL DEFAULT '',
		model_used          TEXT NOT NULL DEFAULT '',
		is_streaming        INTEGER NOT NULL DEFAULT 0,
		input_tokens        INTEGER NOT NULL DEFAULT 0,
		output_tokens       INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
		cache_write_tokens  INTEGER NOT NULL DEFAULT 0,
		total_tokens        INTEGER NOT NULL DEFAULT 0,
		cost_usd            REAL NOT NULL DEFAULT 0,
		started_at          TEXT NOT NULL,
		completed_at        TEXT,
		latency_ms          INTEGER,
		input_cost_usd      REAL NOT NULL DEFAULT 0,
		output_cost_usd     REAL NOT NULL DEFAULT 0,
		cache_read_cost_usd REAL NOT NULL DEFAULT 0,
		cache_write_cost_usd REAL NOT NULL DEFAULT 0,
		pricing_input_per_mtok REAL NOT NULL DEFAULT 0,
		pricing_output_per_mtok REAL NOT NULL DEFAULT 0,
		pricing_cache_read_per_mtok REAL NOT NULL DEFAULT 0,
		pricing_cache_write_per_mtok REAL NOT NULL DEFAULT 0
	)`)

	sessDB.MustExec(`CREATE TABLE IF NOT EXISTS token_usage (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id       TEXT NOT NULL,
		agent_name       TEXT NOT NULL DEFAULT '',
		agent_type       TEXT NOT NULL DEFAULT 'claude',
		team_id          INTEGER,
		board_name       TEXT,
		input_tokens     INTEGER NOT NULL DEFAULT 0,
		output_tokens    INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_write_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens     INTEGER NOT NULL DEFAULT 0,
		cost_usd         REAL NOT NULL DEFAULT 0,
		num_turns        INTEGER NOT NULL DEFAULT 0,
		session_start_at TEXT,
		last_activity_at TEXT,
		recorded_at      TEXT NOT NULL,
		source           TEXT NOT NULL DEFAULT 'jsonl'
	)`)
	sessDB.MustExec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_token_usage_session_time ON token_usage(session_id, recorded_at)`)

	s.SetSessionsDB(sessDB)
	return s, sessDB
}

// insertTokenUsage is a test helper to insert a token usage record into the sessions DB.
func insertTokenUsage(t *testing.T, sessDB *sqlx.DB, sessionID, recordedAt string, costUSD float64, inputTok, outputTok, cacheRead, cacheWrite int) {
	t.Helper()
	_, err := sessDB.Exec(
		`INSERT INTO token_usage (session_id, agent_name, agent_type, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_tokens, cost_usd, recorded_at, source)
		 VALUES (?, '', 'claude', ?, ?, ?, ?, ?, ?, ?, 'proxy')`,
		sessionID, inputTok, outputTok, cacheRead, cacheWrite, inputTok+outputTok+cacheRead+cacheWrite, costUSD, recordedAt)
	require.NoError(t, err)
}

// insertLiveSession is a test helper to insert a live session into the sessions DB.
func insertLiveSession(t *testing.T, sessDB *sqlx.DB, sessionID, agentName, displayName string) {
	t.Helper()
	_, err := sessDB.Exec(
		`INSERT INTO live_sessions (session_id, agent_name, display_name, created_at)
		 VALUES (?, ?, ?, ?)`,
		sessionID, agentName, displayName, nowUTC())
	require.NoError(t, err)
}

func TestClaimTask_SessionIDResolution(t *testing.T) {
	s, sessDB := testStoreWithSessionsDB(t)
	ctx := context.Background()

	// Set up: subscriber "lead-dev" with session_name "tmux-lead-dev".
	// SessionIDFromName("tmux-lead-dev") → "lead-dev", so live session uses that as session_id.
	sub(s, ctx, "proj", "lead-dev", "Lead Developer")
	insertLiveSession(t, sessDB, "lead-dev", "proj", "Lead Developer")

	// Create and claim a task.
	s.CreateTask(ctx, "proj", "Fix the bug", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "lead-dev")
	require.NoError(t, err)
	require.NotNil(t, claimed)

	// session_id should be resolved from live_sessions.
	require.NotNil(t, claimed.SessionID, "session_id should be populated after claim")
	assert.Equal(t, "lead-dev", *claimed.SessionID)
}

func TestClaimTask_SessionIDNull_WhenNoLiveSession(t *testing.T) {
	s, _ := testStoreWithSessionsDB(t)
	ctx := context.Background()

	// Subscriber exists but no matching live session.
	sub(s, ctx, "proj", "ghost-agent", "Ghost")

	s.CreateTask(ctx, "proj", "Orphan task", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "ghost-agent")
	require.NoError(t, err)
	require.NotNil(t, claimed)

	// session_id should be NULL since no live session exists.
	assert.Nil(t, claimed.SessionID, "session_id should be nil when no live session exists")
}

func TestClaimTask_SessionIDNull_WhenNoSessionsDB(t *testing.T) {
	// Board store WITHOUT a sessions DB reference.
	s := testStore(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "bob", "Bob")
	s.CreateTask(ctx, "proj", "Task without sessions DB", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "bob")
	require.NoError(t, err)
	require.NotNil(t, claimed)

	assert.Nil(t, claimed.SessionID, "session_id should be nil when sessionsDB is not set")
}

func TestCompleteTask_CostComputation(t *testing.T) {
	s, sessDB := testStoreWithSessionsDB(t)
	ctx := context.Background()

	// Set up subscriber + live session.
	sub(s, ctx, "proj", "dev", "Developer")
	insertLiveSession(t, sessDB, "dev", "proj", "Developer")

	// Create, claim task.
	s.CreateTask(ctx, "proj", "Build feature", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "dev")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NotNil(t, claimed.SessionID)
	require.NotNil(t, claimed.ClaimedAt)

	// Backdate claimed_at so the window spans a known range.
	// Records at anchor and anchor+1s will be inside; completedAt (now) is well after.
	anchor := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)
	anchor1 := time.Now().Add(-9 * time.Second).UTC().Format(time.RFC3339)
	s.db.ExecContext(ctx, `UPDATE board_tasks SET claimed_at = ? WHERE id = ?`, anchor, claimed.ID)

	insertTokenUsage(t, sessDB, "dev", anchor, 0.05, 1000, 200, 500, 100)
	insertTokenUsage(t, sessDB, "dev", anchor1, 0.03, 2000, 300, 400, 50)

	// Insert a token usage record OUTSIDE the window (before claimed_at) — should NOT be counted.
	tBefore := time.Now().Add(-20 * time.Second).UTC().Format(time.RFC3339)
	insertTokenUsage(t, sessDB, "dev", tBefore, 1.00, 99999, 99999, 99999, 99999)

	// Insert a token usage record for a DIFFERENT session — should NOT be counted.
	insertTokenUsage(t, sessDB, "other-session", anchor, 2.00, 50000, 50000, 50000, 50000)

	// Complete the task.
	completed, err := s.CompleteTask(ctx, "proj", claimed.ID, "dev", nil)
	require.NoError(t, err)
	require.NotNil(t, completed)
	assert.Equal(t, "completed", completed.Status)

	// Verify cost fields are populated from the two in-window requests.
	require.NotNil(t, completed.CostUSD, "cost_usd should be populated")
	assert.InDelta(t, 0.08, *completed.CostUSD, 0.001)

	require.NotNil(t, completed.InputTokens)
	assert.Equal(t, 3000, *completed.InputTokens)

	require.NotNil(t, completed.OutputTokens)
	assert.Equal(t, 500, *completed.OutputTokens)

	require.NotNil(t, completed.CacheReadTokens)
	assert.Equal(t, 900, *completed.CacheReadTokens)

	require.NotNil(t, completed.CacheWriteTokens)
	assert.Equal(t, 150, *completed.CacheWriteTokens)
}

func TestCompleteTask_ZeroCostWhenNoProxyRequests(t *testing.T) {
	s, sessDB := testStoreWithSessionsDB(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "dev", "Developer")
	insertLiveSession(t, sessDB, "dev", "proj", "Developer")

	s.CreateTask(ctx, "proj", "Quick task", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "dev")
	require.NoError(t, err)
	require.NotNil(t, claimed)

	// Complete immediately — no proxy requests exist for this session.
	completed, err := s.CompleteTask(ctx, "proj", claimed.ID, "dev", nil)
	require.NoError(t, err)
	require.NotNil(t, completed)

	// Cost should be 0, not NULL (COALESCE in query handles this).
	require.NotNil(t, completed.CostUSD, "cost_usd should be 0 not nil")
	assert.Equal(t, 0.0, *completed.CostUSD)

	require.NotNil(t, completed.InputTokens)
	assert.Equal(t, 0, *completed.InputTokens)

	require.NotNil(t, completed.OutputTokens)
	assert.Equal(t, 0, *completed.OutputTokens)

	require.NotNil(t, completed.CacheReadTokens)
	assert.Equal(t, 0, *completed.CacheReadTokens)

	require.NotNil(t, completed.CacheWriteTokens)
	assert.Equal(t, 0, *completed.CacheWriteTokens)
}

func TestCancelTask_CostComputation(t *testing.T) {
	s, sessDB := testStoreWithSessionsDB(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "dev", "Developer")
	insertLiveSession(t, sessDB, "dev", "proj", "Developer")

	s.CreateTask(ctx, "proj", "Cancelled feature", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "dev")
	require.NoError(t, err)
	require.NotNil(t, claimed)

	claimedAt := *claimed.ClaimedAt

	// Insert token usage during the task (use claimed_at as timestamp).
	insertTokenUsage(t, sessDB, "dev", claimedAt, 0.12, 5000, 800, 1200, 300)

	// Cancel the task.
	msg := "Not needed anymore"
	cancelled, err := s.CancelTask(ctx, "proj", claimed.ID, "dev", &msg)
	require.NoError(t, err)
	require.NotNil(t, cancelled)
	assert.Equal(t, "skipped", cancelled.Status)

	// Verify cost was still computed (spec: skipped tasks capture cost).
	require.NotNil(t, cancelled.CostUSD)
	assert.InDelta(t, 0.12, *cancelled.CostUSD, 0.001)

	require.NotNil(t, cancelled.InputTokens)
	assert.Equal(t, 5000, *cancelled.InputTokens)

	require.NotNil(t, cancelled.OutputTokens)
	assert.Equal(t, 800, *cancelled.OutputTokens)

	require.NotNil(t, cancelled.CacheReadTokens)
	assert.Equal(t, 1200, *cancelled.CacheReadTokens)

	require.NotNil(t, cancelled.CacheWriteTokens)
	assert.Equal(t, 300, *cancelled.CacheWriteTokens)
}

func TestReassignTask_ClearsSessionID(t *testing.T) {
	s, sessDB := testStoreWithSessionsDB(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "dev-a", "Developer A")
	sub(s, ctx, "proj", "dev-b", "Developer B")
	insertLiveSession(t, sessDB, "dev-a", "proj", "Developer A")
	insertLiveSession(t, sessDB, "dev-b", "proj", "Developer B")

	// Create task assigned to dev-a.
	s.CreateTask(ctx, "proj", "Reassignable task", "", "medium", "admin", "dev-a")

	// dev-a claims it — session_id should be set.
	claimed, err := s.ClaimTask(ctx, "proj", "dev-a")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NotNil(t, claimed.SessionID)
	assert.Equal(t, "dev-a", *claimed.SessionID)

	// Reassign to dev-b — should clear session_id and reset to pending.
	reassigned, err := s.ReassignTask(ctx, "proj", claimed.ID, "dev-b")
	require.NoError(t, err)
	require.NotNil(t, reassigned)
	assert.Equal(t, "pending", reassigned.Status)
	assert.Nil(t, reassigned.SessionID, "session_id should be cleared on reassign")
	assert.Nil(t, reassigned.ClaimedAt, "claimed_at should be cleared on reassign")

	// dev-b claims it — should get dev-b's session_id.
	reclaimed, err := s.ClaimTask(ctx, "proj", "dev-b")
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	require.NotNil(t, reclaimed.SessionID)
	assert.Equal(t, "dev-b", *reclaimed.SessionID)
}

func TestCompleteTask_NoCostWhenNoSessionID(t *testing.T) {
	// If session_id is NULL (no live session at claim time), cost should remain NULL.
	s, _ := testStoreWithSessionsDB(t)
	ctx := context.Background()

	// Subscriber with no matching live session.
	sub(s, ctx, "proj", "orphan", "Orphan Agent")

	s.CreateTask(ctx, "proj", "Orphan task", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "orphan")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Nil(t, claimed.SessionID)

	completed, err := s.CompleteTask(ctx, "proj", claimed.ID, "orphan", nil)
	require.NoError(t, err)
	require.NotNil(t, completed)

	// Cost fields should remain NULL since we couldn't resolve a session.
	assert.Nil(t, completed.CostUSD, "cost_usd should be nil when session_id is nil")
	assert.Nil(t, completed.InputTokens)
}

// ── Live Cost Tests ─────────────────────────────────────────────────────

func TestGetTaskLiveCost_InProgressTask(t *testing.T) {
	s, sessDB := testStoreWithSessionsDB(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "dev", "Developer")
	insertLiveSession(t, sessDB, "dev", "proj", "Developer")

	s.CreateTask(ctx, "proj", "In progress work", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "dev")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.NotNil(t, claimed.SessionID)

	// Insert token usage after claim.
	// Backdate claimed_at so we can use two records at known timestamps.
	anchor := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)
	anchor1 := time.Now().Add(-9 * time.Second).UTC().Format(time.RFC3339)
	s.db.ExecContext(ctx, `UPDATE board_tasks SET claimed_at = ? WHERE id = ?`, anchor, claimed.ID)

	insertTokenUsage(t, sessDB, "dev", anchor, 0.10, 3000, 500, 800, 200)
	insertTokenUsage(t, sessDB, "dev", anchor1, 0.05, 1000, 100, 200, 50)

	// Query live cost — task is still in_progress.
	cost, err := s.GetTaskLiveCost(ctx, "proj", claimed.ID)
	require.NoError(t, err)
	require.NotNil(t, cost)

	assert.Equal(t, claimed.ID, cost.TaskID)
	assert.Equal(t, "dev", cost.SessionID)
	assert.InDelta(t, 0.15, cost.CostUSD, 0.001)
	assert.Equal(t, 4000, cost.InputTokens)
	assert.Equal(t, 600, cost.OutputTokens)
	assert.Equal(t, 1000, cost.CacheReadTokens)
	assert.Equal(t, 250, cost.CacheWriteTokens)
	assert.Equal(t, 2, cost.RequestCount)
}

func TestGetTaskLiveCost_ExcludesOldRequests(t *testing.T) {
	s, sessDB := testStoreWithSessionsDB(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "dev", "Developer")
	insertLiveSession(t, sessDB, "dev", "proj", "Developer")

	// Insert a token usage record BEFORE any task is claimed.
	oldTime := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	insertTokenUsage(t, sessDB, "dev", oldTime, 5.00, 99999, 99999, 99999, 99999)

	s.CreateTask(ctx, "proj", "Fresh work", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "dev")
	require.NoError(t, err)
	require.NotNil(t, claimed)

	// Insert one record after claim.
	claimedAt := *claimed.ClaimedAt
	insertTokenUsage(t, sessDB, "dev", claimedAt, 0.02, 500, 100, 0, 0)

	cost, err := s.GetTaskLiveCost(ctx, "proj", claimed.ID)
	require.NoError(t, err)
	require.NotNil(t, cost)

	// Only the post-claim request should be counted.
	assert.InDelta(t, 0.02, cost.CostUSD, 0.001)
	assert.Equal(t, 500, cost.InputTokens)
	assert.Equal(t, 1, cost.RequestCount)
}

func TestGetTaskLiveCost_NilWhenNoSessionID(t *testing.T) {
	s, _ := testStoreWithSessionsDB(t)
	ctx := context.Background()

	// Subscriber with no live session — session_id will be NULL.
	sub(s, ctx, "proj", "orphan", "Orphan")
	s.CreateTask(ctx, "proj", "No session task", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "orphan")
	require.NoError(t, err)
	require.NotNil(t, claimed)

	cost, err := s.GetTaskLiveCost(ctx, "proj", claimed.ID)
	require.NoError(t, err)
	assert.Nil(t, cost, "live cost should be nil when session_id is nil")
}

func TestGetTaskLiveCost_NilWhenNoSessionsDB(t *testing.T) {
	s := testStore(t) // No sessions DB wired
	ctx := context.Background()

	sub(s, ctx, "proj", "dev", "Dev")
	s.CreateTask(ctx, "proj", "Unwired task", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "dev")
	require.NoError(t, err)

	cost, err := s.GetTaskLiveCost(ctx, "proj", claimed.ID)
	require.NoError(t, err)
	assert.Nil(t, cost, "live cost should be nil when sessions DB not configured")
}

func TestGetTaskLiveCost_TaskNotFound(t *testing.T) {
	s, _ := testStoreWithSessionsDB(t)
	ctx := context.Background()

	cost, err := s.GetTaskLiveCost(ctx, "proj", 9999)
	assert.Error(t, err)
	assert.Nil(t, cost)
}

func TestGetTaskLiveCost_ZeroWhenNoRequests(t *testing.T) {
	s, sessDB := testStoreWithSessionsDB(t)
	ctx := context.Background()

	sub(s, ctx, "proj", "dev", "Developer")
	insertLiveSession(t, sessDB, "dev", "proj", "Developer")

	s.CreateTask(ctx, "proj", "Zero cost task", "", "medium", "admin")
	claimed, err := s.ClaimTask(ctx, "proj", "dev")
	require.NoError(t, err)
	require.NotNil(t, claimed)

	cost, err := s.GetTaskLiveCost(ctx, "proj", claimed.ID)
	require.NoError(t, err)
	require.NotNil(t, cost)

	assert.Equal(t, 0.0, cost.CostUSD)
	assert.Equal(t, 0, cost.InputTokens)
	assert.Equal(t, 0, cost.RequestCount)
}
