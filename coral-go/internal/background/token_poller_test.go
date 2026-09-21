package background

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Sample JSONL Data ───────────────────────────────────────

const sampleClaudeJSONL = `{"type":"user","message":{"role":"user","content":"hello"},"timestamp":"2026-04-06T10:00:00Z"}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"Hi there!"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":500,"cache_read_input_tokens":200}},"timestamp":"2026-04-06T10:00:05Z"}
{"type":"user","message":{"role":"user","content":"explain Go channels"},"timestamp":"2026-04-06T10:00:10Z"}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"Go channels are..."}],"usage":{"input_tokens":300,"output_tokens":150,"cache_creation_input_tokens":0,"cache_read_input_tokens":700}},"timestamp":"2026-04-06T10:00:20Z"}
`

// writeTestJSONL creates a temporary JSONL file with the given content.
func writeTestJSONL(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test-session.jsonl")
	err := os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err)
	return path
}

// ── extractClaudeUsage Tests ────────────────────────────────

func TestExtractClaudeUsage_HappyPath(t *testing.T) {
	path := writeTestJSONL(t, sampleClaudeJSONL)

	usage := extractClaudeUsage(path)
	require.NotNil(t, usage)
	require.Len(t, usage.Calls, 2)

	// First assistant turn
	assert.Equal(t, 100, usage.Calls[0].InputTokens)
	assert.Equal(t, 50, usage.Calls[0].OutputTokens)
	assert.Equal(t, 500, usage.Calls[0].CacheWriteTokens)
	assert.Equal(t, 200, usage.Calls[0].CacheReadTokens)
	assert.Equal(t, "claude-sonnet-4-6", usage.Calls[0].Model)
	assert.Equal(t, "2026-04-06T10:00:05Z", usage.Calls[0].Timestamp)

	// Second assistant turn
	assert.Equal(t, 300, usage.Calls[1].InputTokens)
	assert.Equal(t, 150, usage.Calls[1].OutputTokens)
	assert.Equal(t, 0, usage.Calls[1].CacheWriteTokens)
	assert.Equal(t, 700, usage.Calls[1].CacheReadTokens)
	assert.Equal(t, "2026-04-06T10:00:20Z", usage.Calls[1].Timestamp)
}

func TestExtractClaudeUsage_SkipsNonAssistantEntries(t *testing.T) {
	jsonl := `{"type":"user","message":{"role":"user","content":"hello"},"timestamp":"2026-04-06T10:00:00Z"}
{"type":"system","message":{"role":"system","content":"you are helpful"},"timestamp":"2026-04-06T10:00:00Z"}
{"type":"progress","timestamp":"2026-04-06T10:00:01Z"}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"Hi!"}],"usage":{"input_tokens":50,"output_tokens":25}},"timestamp":"2026-04-06T10:00:02Z"}
`
	path := writeTestJSONL(t, jsonl)

	usage := extractClaudeUsage(path)
	require.NotNil(t, usage)
	require.Len(t, usage.Calls, 1, "only assistant entries should be extracted")
	assert.Equal(t, 50, usage.Calls[0].InputTokens)
}

func TestExtractClaudeUsage_SkipsAssistantWithoutUsage(t *testing.T) {
	jsonl := `{"type":"assistant","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"Hi!"}]},"timestamp":"2026-04-06T10:00:01Z"}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"More"}],"usage":{"input_tokens":100,"output_tokens":50}},"timestamp":"2026-04-06T10:00:02Z"}
`
	path := writeTestJSONL(t, jsonl)

	usage := extractClaudeUsage(path)
	require.NotNil(t, usage)
	require.Len(t, usage.Calls, 1, "assistant without usage should be skipped")
	assert.Equal(t, 100, usage.Calls[0].InputTokens)
}

func TestExtractClaudeUsage_HandlesMalformedLines(t *testing.T) {
	jsonl := `not valid json at all
{"type":"assistant","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":50}},"timestamp":"2026-04-06T10:00:01Z"}
{broken json
{"type":"assistant","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":200,"output_tokens":100}},"timestamp":"2026-04-06T10:00:02Z"}
`
	path := writeTestJSONL(t, jsonl)

	usage := extractClaudeUsage(path)
	require.NotNil(t, usage)
	assert.Len(t, usage.Calls, 2, "malformed lines should be skipped gracefully")
}

func TestExtractClaudeUsage_ModelExtraction(t *testing.T) {
	jsonl := `{"type":"assistant","message":{"model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"Hi!"}],"usage":{"input_tokens":100,"output_tokens":50}},"timestamp":"2026-04-06T10:00:01Z"}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","role":"assistant","content":[{"type":"text","text":"Fast!"}],"usage":{"input_tokens":50,"output_tokens":25}},"timestamp":"2026-04-06T10:00:02Z"}
`
	path := writeTestJSONL(t, jsonl)

	usage := extractClaudeUsage(path)
	require.NotNil(t, usage)
	require.Len(t, usage.Calls, 2)
	assert.Equal(t, "claude-opus-4-6", usage.Calls[0].Model)
	assert.Equal(t, "claude-haiku-4-5-20251001", usage.Calls[1].Model)
}

func TestPollClaudeSession_LearnsModelAndContextWindow(t *testing.T) {
	db := setupTestDB(t)
	ss := store.NewSessionStore(db)
	us := store.NewTokenUsageStore(db)
	ctx := context.Background()
	const sid = "00000000-0000-0000-0000-000000000097"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: "coral-go", WorkingDir: t.TempDir(),
	}))

	path := writeTestJSONL(t, `{"type":"assistant","message":{"model":"claude-fable-5-1","usage":{"input_tokens":32,"output_tokens":100,"cache_creation_input_tokens":650,"cache_read_input_tokens":292368}},"timestamp":"2026-09-16T07:39:26Z"}`+"\n")
	poller := NewTokenPoller(ss, us, time.Hour)
	poller.sessionPaths[sid] = path
	ls, err := ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	require.NotNil(t, ls)
	poller.pollClaudeSession(ctx, ls)

	learned, err := ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	require.NotNil(t, learned.Model)
	assert.Equal(t, "claude-fable-5-1", *learned.Model)
	assert.Equal(t, 1_000_000, learned.ContextWindow)
	latest, err := us.GetLatestTurnContextBySessionIDs(ctx, []string{sid})
	require.NoError(t, err)
	assert.Equal(t, 293_050, latest[sid], "numerator remains input + cache read + cache write")
}

func TestExtractClaudeUsage_CacheTokens(t *testing.T) {
	jsonl := `{"type":"assistant","message":{"model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":1000,"cache_read_input_tokens":500}},"timestamp":"2026-04-06T10:00:01Z"}
`
	path := writeTestJSONL(t, jsonl)

	usage := extractClaudeUsage(path)
	require.NotNil(t, usage)
	require.Len(t, usage.Calls, 1)
	assert.Equal(t, 1000, usage.Calls[0].CacheWriteTokens, "cache_creation_input_tokens → CacheWriteTokens")
	assert.Equal(t, 500, usage.Calls[0].CacheReadTokens, "cache_read_input_tokens → CacheReadTokens")
}

func TestExtractClaudeUsage_EmptyFile(t *testing.T) {
	path := writeTestJSONL(t, "")

	usage := extractClaudeUsage(path)
	assert.Nil(t, usage, "empty file should return nil")
}

func TestExtractClaudeUsage_NoAssistantEntries(t *testing.T) {
	jsonl := `{"type":"user","message":{"role":"user","content":"hello"},"timestamp":"2026-04-06T10:00:00Z"}
{"type":"user","message":{"role":"user","content":"world"},"timestamp":"2026-04-06T10:00:05Z"}
`
	path := writeTestJSONL(t, jsonl)

	usage := extractClaudeUsage(path)
	assert.Nil(t, usage, "file with no assistant entries should return nil")
}

func TestExtractClaudeUsage_NonexistentFile(t *testing.T) {
	usage := extractClaudeUsage("/nonexistent/path/to/file.jsonl")
	assert.Nil(t, usage)
}

// ── Cost Estimation Tests ───────────────────────────────────

func TestEstimateCost_ClaudeModels(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		input      int
		output     int
		cacheRead  int
		cacheWrite int
		minCost    float64
	}{
		{
			name:  "claude-sonnet-4-6",
			model: "claude-sonnet-4-6",
			input: 1_000_000, output: 1_000_000, cacheRead: 0, cacheWrite: 0,
			minCost: 3.00 + 15.00, // $3/M input + $15/M output
		},
		{
			name:  "claude-opus-4-6",
			model: "claude-opus-4-6",
			input: 1_000_000, output: 1_000_000, cacheRead: 0, cacheWrite: 0,
			minCost: 5.00 + 25.00,
		},
		{
			name:  "claude-sonnet with cache read",
			model: "claude-sonnet-4-6",
			input: 1000, output: 500, cacheRead: 10000, cacheWrite: 0,
			minCost: 0, // just verify it's > 0
		},
		{
			name:  "claude-sonnet with cache write",
			model: "claude-sonnet-4-6",
			input: 1_000_000, output: 1_000_000, cacheRead: 0, cacheWrite: 1_000_000,
			minCost: 3.00 + 15.00 + 3.75, // $3/M input + $15/M output + $3.75/M cache write
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := estimateCost(tt.model, tt.input, tt.output, tt.cacheRead, tt.cacheWrite)
			if tt.minCost > 0 {
				assert.InDelta(t, tt.minCost, cost, 0.01)
			} else {
				assert.Greater(t, cost, 0.0)
			}
		})
	}
}

func TestEstimateCost_UnknownModel(t *testing.T) {
	cost := estimateCost("unknown-model-xyz", 1000, 500, 0, 0)
	assert.Equal(t, 0.0, cost, "unknown model should return zero cost")
}

// ── extractCodexUsage Tests (existing, for completeness) ────

func TestExtractCodexUsage_EmptyFile(t *testing.T) {
	path := writeTestJSONL(t, "")
	usage := extractCodexUsage(path)
	assert.Nil(t, usage)
}

func TestExtractCodexUsage_NonexistentFile(t *testing.T) {
	usage := extractCodexUsage("/nonexistent/path.jsonl")
	assert.Nil(t, usage)
}

func TestExtractCodexUsage_NoTokenCounts(t *testing.T) {
	jsonl := `{"type":"session_meta","timestamp":"2026-01-01T00:00:00Z","payload":{"cwd":"/tmp"}}
{"type":"turn_context","payload":{"model":"gpt-4o","cwd":"/tmp"}}
`
	path := writeTestJSONL(t, jsonl)
	usage := extractCodexUsage(path)
	assert.Nil(t, usage)
}

// ── model recorded per usage row ─────────────────────────────

type usageRow struct {
	RecordedAt string  `db:"recorded_at"`
	Model      *string `db:"model"`
	CostUSD    float64 `db:"cost_usd"`
}

func usageRows(t *testing.T, db *store.DB, sid string) []usageRow {
	t.Helper()
	var rows []usageRow
	require.NoError(t, db.SelectContext(context.Background(), &rows,
		"SELECT recorded_at, model, cost_usd FROM token_usage WHERE session_id = ? ORDER BY recorded_at", sid))
	return rows
}

func registerForPoll(t *testing.T, db *store.DB, sid, agentType string) (*store.SessionStore, *store.LiveSession) {
	t.Helper()
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: sid, AgentType: agentType, AgentName: "coral-go", WorkingDir: t.TempDir()}))
	ls, err := ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	return ss, ls
}

// A session that switches model part-way records each row under the model
// that produced it, which is what makes the stored cost re-derivable.
func TestPollClaudeSession_RecordsModelPerCall(t *testing.T) {
	db := setupTestDB(t)
	const sid = "00000000-0000-0000-0000-0000000000c1"
	ss, ls := registerForPoll(t, db, sid, "claude")
	path := writeTestJSONL(t, `{"type":"assistant","message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":1000,"output_tokens":1000}},"timestamp":"2026-09-21T10:00:01Z"}
{"type":"assistant","message":{"id":"m2","model":"claude-fable-5-1","usage":{"input_tokens":1000,"output_tokens":1000}},"timestamp":"2026-09-21T10:00:02Z"}
`)
	poller := NewTokenPoller(ss, store.NewTokenUsageStore(db), time.Hour)
	poller.sessionPaths[sid] = path
	poller.pollClaudeSession(context.Background(), ls)

	rows := usageRows(t, db, sid)
	require.Len(t, rows, 2)
	require.NotNil(t, rows[0].Model)
	require.NotNil(t, rows[1].Model)
	assert.Equal(t, "claude-opus-5", *rows[0].Model)
	assert.Equal(t, "claude-fable-5-1", *rows[1].Model)
	// Each row's cost matches its own model: opus $5+$25, fable $10+$50 per million.
	assert.InDelta(t, 0.030, rows[0].CostUSD, 1e-9)
	assert.InDelta(t, 0.060, rows[1].CostUSD, 1e-9)
}

// With no model in the transcript the poller still prices the call using a
// default, but must not write that default into the model column.
func TestPollClaudeSession_MissingModelIsStoredAsNullNotTheFallback(t *testing.T) {
	db := setupTestDB(t)
	const sid = "00000000-0000-0000-0000-0000000000c2"
	ss, ls := registerForPoll(t, db, sid, "claude")
	path := writeTestJSONL(t, `{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":1000000,"output_tokens":0}},"timestamp":"2026-09-21T10:00:01Z"}`+"\n")
	poller := NewTokenPoller(ss, store.NewTokenUsageStore(db), time.Hour)
	poller.sessionPaths[sid] = path
	poller.pollClaudeSession(context.Background(), ls)

	rows := usageRows(t, db, sid)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].Model, "the pricing fallback is a guess and must not be recorded as the model")
	assert.InDelta(t, 3.00, rows[0].CostUSD, 1e-9, "still priced, at the default model's rate")
}

func TestPollCodexSession_RecordsModelIncludingMidSessionSwitch(t *testing.T) {
	db := setupTestDB(t)
	const sid = "00000000-0000-0000-0000-0000000000c3"
	ss, ls := registerForPoll(t, db, sid, "codex")
	tokenCount := func(ts string, input, cached, output int) string {
		return fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"total_tokens":%d}}}}`,
			ts, input, cached, output, input+output)
	}
	path := writeTestJSONL(t, strings.Join([]string{
		`{"type":"session_meta","timestamp":"2026-09-21T10:00:00Z","payload":{}}`,
		`{"type":"turn_context","timestamp":"2026-09-21T10:00:00Z","payload":{"model":"gpt-5.6-sol"}}`,
		tokenCount("2026-09-21T10:00:01Z", 1_000_000, 900_000, 0),
		`{"type":"turn_context","timestamp":"2026-09-21T10:00:02Z","payload":{"model":"gpt-5.6-luna"}}`,
		tokenCount("2026-09-21T10:00:03Z", 2_000_000, 900_000, 0), // cumulative: +1M input, +0 cached
	}, "\n")+"\n")

	poller := NewTokenPoller(ss, store.NewTokenUsageStore(db), time.Hour)
	poller.sessionPaths[sid] = path
	poller.pollCodexSession(context.Background(), ls)

	rows := usageRows(t, db, sid)
	require.Len(t, rows, 2)
	require.NotNil(t, rows[0].Model)
	require.NotNil(t, rows[1].Model)
	assert.Equal(t, "gpt-5.6-sol", *rows[0].Model)
	assert.Equal(t, "gpt-5.6-luna", *rows[1].Model)
	// sol: 100k uncached at $4 + 900k cached at $0.40 = $0.76. luna: 1M at $0.20.
	assert.InDelta(t, 0.76, rows[0].CostUSD, 1e-9)
	assert.InDelta(t, 0.20, rows[1].CostUSD, 1e-9)
}
