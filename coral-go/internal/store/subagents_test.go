package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	mainAgentA = "aaaaaaaa-0000-0000-0000-000000000001"
	mainAgentB = "bbbbbbbb-0000-0000-0000-000000000002"
)

func sp(s string) *string { return &s }

// newSubagentFixture returns a store with two registered main agents, since
// every subagent must reference one.
func newSubagentFixture(t *testing.T) (*SubagentStore, *SessionStore, *DB) {
	t.Helper()
	db := openTestDB(t)
	ss := NewSessionStore(db)
	for _, sid := range []string{mainAgentA, mainAgentB} {
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &LiveSession{
			SessionID: sid, AgentType: "claude", AgentName: "coral-go", WorkingDir: "/repo",
		}))
	}
	return NewSubagentStore(db), ss, db
}

func countRows(t *testing.T, db *DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.GetContext(context.Background(), &n, "SELECT COUNT(*) FROM "+table))
	return n
}

func TestSubagentStore_UpsertAndGetRoundTrip(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	ctx := context.Background()

	in := &Subagent{
		SessionID: mainAgentA, SubagentID: "aca6223d75309e7ca",
		SubagentType: sp("Explore"), Description: sp("Map Coral task-board UI code"),
		ToolUseID: sp("toolu_016f"), Model: sp("claude-opus-5"), SpawnDepth: 1,
		APICalls: 43, InputTokens: 86, OutputTokens: 18929,
		CacheReadTokens: 3881709, CacheWriteTokens: 261196, CostUSD: 12.1396,
		StartedAt: sp("2026-09-17T02:10:18.133Z"), LastActivityAt: sp("2026-09-17T02:14:11.667Z"),
	}
	require.NoError(t, st.UpsertSubagent(ctx, in))

	got, err := st.GetSubagent(ctx, mainAgentA, "aca6223d75309e7ca")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.NotZero(t, got.ID)
	assert.Equal(t, mainAgentA, got.SessionID)
	assert.Equal(t, "aca6223d75309e7ca", got.SubagentID)
	assert.Equal(t, "Explore", *got.SubagentType)
	assert.Equal(t, "Map Coral task-board UI code", *got.Description)
	assert.Equal(t, "toolu_016f", *got.ToolUseID)
	assert.Equal(t, "claude-opus-5", *got.Model)
	assert.Equal(t, 1, got.SpawnDepth)
	assert.Equal(t, 43, got.APICalls)
	assert.Equal(t, 86, got.InputTokens)
	assert.Equal(t, 18929, got.OutputTokens)
	assert.Equal(t, 3881709, got.CacheReadTokens)
	assert.Equal(t, 261196, got.CacheWriteTokens)
	assert.InDelta(t, 12.1396, got.CostUSD, 1e-9)
	assert.Equal(t, "2026-09-17T02:10:18.133Z", *got.StartedAt)
	assert.Equal(t, "2026-09-17T02:14:11.667Z", *got.LastActivityAt)
	assert.NotEmpty(t, got.CreatedAt)
	assert.NotEmpty(t, got.UpdatedAt)
}

func TestSubagentStore_GetMissingReturnsNil(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	got, err := st.GetSubagent(context.Background(), mainAgentA, "nope")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// Usage fields are cumulative totals that REPLACE the stored values. If they
// were added instead, every poll (and every server restart, which re-reads all
// transcripts) would inflate the recorded spend.
func TestSubagentStore_UpsertReplacesTotalsRatherThanAdding(t *testing.T) {
	st, _, db := newSubagentFixture(t)
	ctx := context.Background()
	row := func(calls, out int, cost float64) *Subagent {
		return &Subagent{SessionID: mainAgentA, SubagentID: "s1", APICalls: calls, InputTokens: calls * 2,
			OutputTokens: out, CacheReadTokens: out * 10, CacheWriteTokens: out * 3, CostUSD: cost}
	}

	require.NoError(t, st.UpsertSubagent(ctx, row(10, 1000, 1.5)))
	require.NoError(t, st.UpsertSubagent(ctx, row(10, 1000, 1.5))) // identical re-sync
	got, err := st.GetSubagent(ctx, mainAgentA, "s1")
	require.NoError(t, err)
	assert.Equal(t, 10, got.APICalls)
	assert.Equal(t, 1000, got.OutputTokens)
	assert.InDelta(t, 1.5, got.CostUSD, 1e-9)

	require.NoError(t, st.UpsertSubagent(ctx, row(25, 4000, 6.25))) // transcript grew
	got, err = st.GetSubagent(ctx, mainAgentA, "s1")
	require.NoError(t, err)
	assert.Equal(t, 25, got.APICalls)
	assert.Equal(t, 50, got.InputTokens)
	assert.Equal(t, 4000, got.OutputTokens)
	assert.Equal(t, 40000, got.CacheReadTokens)
	assert.Equal(t, 12000, got.CacheWriteTokens)
	assert.InDelta(t, 6.25, got.CostUSD, 1e-9)

	assert.Equal(t, 1, countRows(t, db, "subagents"), "three upserts of one subagent are one row")
}

func TestSubagentStore_UpsertKeepsRowIdentity(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1"}))
	first, err := st.GetSubagent(ctx, mainAgentA, "s1")
	require.NoError(t, err)
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1", APICalls: 5}))
	second, err := st.GetSubagent(ctx, mainAgentA, "s1")
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.CreatedAt, second.CreatedAt)
}

// The transcript can be synced before Claude has written the metadata file.
// A later sync that lacks a value must not erase one already stored.
func TestSubagentStore_EmptyMetadataDoesNotBlankStoredMetadata(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	ctx := context.Background()

	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1", APICalls: 1}))
	got, err := st.GetSubagent(ctx, mainAgentA, "s1")
	require.NoError(t, err)
	assert.Nil(t, got.Description, "empty metadata is stored as NULL, not as an empty string")
	assert.Nil(t, got.SubagentType)
	assert.Nil(t, got.Model)

	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1", APICalls: 2,
		SubagentType: sp("Explore"), Description: sp("Map UI"), ToolUseID: sp("toolu_1"), Model: sp("claude-opus-5")}))
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1", APICalls: 3,
		SubagentType: sp(""), Description: nil}))

	got, err = st.GetSubagent(ctx, mainAgentA, "s1")
	require.NoError(t, err)
	assert.Equal(t, 3, got.APICalls, "usage still updates")
	assert.Equal(t, "Explore", *got.SubagentType)
	assert.Equal(t, "Map UI", *got.Description)
	assert.Equal(t, "toolu_1", *got.ToolUseID)
	assert.Equal(t, "claude-opus-5", *got.Model)
}

func TestSubagentStore_StartedAtIsStickyLastActivityAdvances(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1",
		StartedAt: sp("2026-01-01T00:00:00Z"), LastActivityAt: sp("2026-01-01T00:01:00Z")}))
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1",
		StartedAt: sp("2026-01-01T09:99:99Z"), LastActivityAt: sp("2026-01-01T00:05:00Z")}))

	got, err := st.GetSubagent(ctx, mainAgentA, "s1")
	require.NoError(t, err)
	assert.Equal(t, "2026-01-01T00:00:00Z", *got.StartedAt)
	assert.Equal(t, "2026-01-01T00:05:00Z", *got.LastActivityAt)
}

// ── Relation to the main agent ───────────────────────────────

func TestSubagentStore_RejectsSubagentOfUnknownMainAgent(t *testing.T) {
	st, _, db := newSubagentFixture(t)
	err := st.UpsertSubagent(context.Background(), &Subagent{
		SessionID: "ffffffff-0000-0000-0000-00000000dead", SubagentID: "s1", APICalls: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FOREIGN KEY")
	assert.Equal(t, 0, countRows(t, db, "subagents"))
}

func TestSubagentStore_RequiresBothIDs(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	ctx := context.Background()
	require.Error(t, st.UpsertSubagent(ctx, &Subagent{SessionID: "", SubagentID: "s1"}))
	require.Error(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: ""}))
}

// A stopped main agent is marked inactive, not deleted, and its subagents'
// spend must survive that.
func TestSubagentStore_SubagentsSurviveMainAgentStop(t *testing.T) {
	st, ss, _ := newSubagentFixture(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1", CostUSD: 2}))
	require.NoError(t, ss.UnregisterLiveSession(ctx, mainAgentA))

	rows, err := st.ListSubagentsBySession(ctx, mainAgentA)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	// ...and a late sync for the stopped agent is still accepted.
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1", CostUSD: 3}))
}

func TestSubagentStore_DeletingMainAgentCascades(t *testing.T) {
	st, _, db := newSubagentFixture(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1"}))
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s2"}))
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentB, SubagentID: "s3"}))

	_, err := db.ExecContext(ctx, "DELETE FROM live_sessions WHERE session_id = ?", mainAgentA)
	require.NoError(t, err)

	a, err := st.ListSubagentsBySession(ctx, mainAgentA)
	require.NoError(t, err)
	assert.Empty(t, a, "no orphans left behind")
	b, err := st.ListSubagentsBySession(ctx, mainAgentB)
	require.NoError(t, err)
	assert.Len(t, b, 1, "the other main agent's subagents are untouched")
}

func TestSubagentStore_SameSubagentIDUnderDifferentMainAgents(t *testing.T) {
	st, _, db := newSubagentFixture(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "shared", OutputTokens: 100}))
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentB, SubagentID: "shared", OutputTokens: 900}))

	a, err := st.GetSubagent(ctx, mainAgentA, "shared")
	require.NoError(t, err)
	b, err := st.GetSubagent(ctx, mainAgentB, "shared")
	require.NoError(t, err)
	assert.Equal(t, 100, a.OutputTokens)
	assert.Equal(t, 900, b.OutputTokens)
	assert.Equal(t, 2, countRows(t, db, "subagents"))
}

// Subagents live in their own table. Recording them must not create rows
// that would make them look like Coral-managed agents, tasks, or main-agent
// token usage.
func TestSubagentStore_DoesNotTouchAgentTables(t *testing.T) {
	st, _, db := newSubagentFixture(t)
	ctx := context.Background()
	before := map[string]int{}
	tables := []string{"live_sessions", "agent_tasks", "token_usage", "agent_events", "session_meta"}
	for _, tbl := range tables {
		before[tbl] = countRows(t, db, tbl)
	}

	for _, id := range []string{"s1", "s2", "s3"} {
		require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: id,
			SubagentType: sp("Explore"), Description: sp("work"), APICalls: 4, OutputTokens: 50, CostUSD: 0.5}))
	}

	assert.Equal(t, 3, countRows(t, db, "subagents"))
	for _, tbl := range tables {
		assert.Equal(t, before[tbl], countRows(t, db, tbl), "table %s must be unaffected", tbl)
	}
}

// ── Listing and aggregation ──────────────────────────────────

func TestSubagentStore_ListIsScopedAndOrderedByStart(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "late", StartedAt: sp("2026-01-01T00:09:00Z")}))
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "early", StartedAt: sp("2026-01-01T00:01:00Z")}))
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentB, SubagentID: "other", StartedAt: sp("2026-01-01T00:00:00Z")}))

	rows, err := st.ListSubagentsBySession(ctx, mainAgentA)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "early", rows[0].SubagentID)
	assert.Equal(t, "late", rows[1].SubagentID)

	none, err := st.ListSubagentsBySession(ctx, "no-such-session")
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestSubagentStore_GetSubagentSpendSumsPerMainAgent(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1",
		APICalls: 65, InputTokens: 130, OutputTokens: 29648, CacheReadTokens: 5431893, CacheWriteTokens: 203930, CostUSD: 14.20}))
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s2",
		APICalls: 43, InputTokens: 86, OutputTokens: 18929, CacheReadTokens: 3881709, CacheWriteTokens: 261196, CostUSD: 12.14}))
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentB, SubagentID: "s3",
		APICalls: 1, OutputTokens: 7, CostUSD: 0.01}))

	spend, err := st.GetSubagentSpend(ctx, []string{mainAgentA, mainAgentB, "session-with-none"})
	require.NoError(t, err)
	require.Len(t, spend, 2, "a session without subagents is absent, not a zero row")

	a := spend[mainAgentA]
	assert.Equal(t, 2, a.Subagents)
	assert.Equal(t, 108, a.APICalls)
	assert.Equal(t, 216, a.InputTokens)
	assert.Equal(t, 48577, a.OutputTokens)
	assert.Equal(t, 9313602, a.CacheReadTokens)
	assert.Equal(t, 465126, a.CacheWriteTokens)
	assert.InDelta(t, 26.34, a.CostUSD, 1e-9)
	assert.Equal(t, 1, spend[mainAgentB].Subagents)

	only, err := st.GetSubagentSpend(ctx, []string{mainAgentB})
	require.NoError(t, err)
	assert.Len(t, only, 1)
}

func TestSubagentStore_GetSubagentSpendEmptyInput(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	spend, err := st.GetSubagentSpend(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, spend)
}

// An existing database gains the table on open, without disturbing its data.
func TestSubagentSchema_CreatedOnReopenOfExistingDB(t *testing.T) {
	path := t.TempDir() + "/existing.db"
	db, err := Open(path)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, NewSessionStore(db).RegisterLiveSession(ctx, &LiveSession{
		SessionID: mainAgentA, AgentType: "claude", AgentName: "coral-go", WorkingDir: "/repo"}))
	_, err = db.ExecContext(ctx, "DROP TABLE subagents") // simulate a DB from before this table existed
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = Open(path)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, NewSubagentStore(db).UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1"}))
	ls, err := NewSessionStore(db).GetLiveSession(ctx, mainAgentA)
	require.NoError(t, err)
	require.NotNil(t, ls, "pre-existing data is intact")
}

func TestSubagentStore_FinishedRoundTripsAndCanReopen(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	ctx := context.Background()
	for _, want := range []bool{false, true, false} { // running → finished → resumed
		require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1", Finished: want}))
		got, err := st.GetSubagent(ctx, mainAgentA, "s1")
		require.NoError(t, err)
		assert.Equal(t, want, got.Finished)
	}
}

// A database created by a build that had the subagents table but not yet the
// finished column must gain the column on open, keeping its rows.
func TestSubagentSchema_FinishedColumnIsMigratedIn(t *testing.T) {
	path := t.TempDir() + "/older.db"
	db, err := Open(path)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, NewSessionStore(db).RegisterLiveSession(ctx, &LiveSession{
		SessionID: mainAgentA, AgentType: "claude", AgentName: "coral-go", WorkingDir: "/repo"}))
	require.NoError(t, NewSubagentStore(db).UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1", OutputTokens: 77}))
	_, err = db.ExecContext(ctx, "ALTER TABLE subagents DROP COLUMN finished")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = Open(path)
	require.NoError(t, err)
	defer db.Close()
	st := NewSubagentStore(db)
	got, err := st.GetSubagent(ctx, mainAgentA, "s1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 77, got.OutputTokens, "existing row survives")
	assert.False(t, got.Finished, "defaults to not finished")
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1", Finished: true}))
}

func TestSubagentStore_TranscriptPathIsStoredButNeverSerialised(t *testing.T) {
	st, _, _ := newSubagentFixture(t)
	ctx := context.Background()
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1",
		TranscriptPath: sp("/home/u/.claude/projects/p/sess/subagents/agent-s1.jsonl")}))
	// A later sync without a path keeps the stored one.
	require.NoError(t, st.UpsertSubagent(ctx, &Subagent{SessionID: mainAgentA, SubagentID: "s1"}))

	got, err := st.GetSubagent(ctx, mainAgentA, "s1")
	require.NoError(t, err)
	require.NotNil(t, got.TranscriptPath)
	assert.Equal(t, "/home/u/.claude/projects/p/sess/subagents/agent-s1.jsonl", *got.TranscriptPath)

	b, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "transcript", "a filesystem path must not leak to API clients")
	assert.NotContains(t, string(b), "/home/u")
}
