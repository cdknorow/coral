package background

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/subagent"
)

const (
	subMainSID    = "11111111-2222-3333-4444-555555555555"
	subFixtureID  = "a1b2c3d4e5f6a7b8c"
	subFixtureDir = "../subagent/testdata/session-dir/subagents"
)

// subagentEnv is a main agent registered in a real DB plus a Claude project
// directory on disk holding its transcript.
type subagentEnv struct {
	t          *testing.T
	db         *store.DB
	sessions   *store.SessionStore
	usage      *store.TokenUsageStore
	subagents  *store.SubagentStore
	parentPath string
}

func newSubagentEnv(t *testing.T) *subagentEnv {
	t.Helper()
	db := setupTestDB(t)
	env := &subagentEnv{
		t: t, db: db,
		sessions:   store.NewSessionStore(db),
		usage:      store.NewTokenUsageStore(db),
		subagents:  store.NewSubagentStore(db),
		parentPath: filepath.Join(t.TempDir(), subMainSID+".jsonl"),
	}
	require.NoError(t, env.sessions.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: subMainSID, AgentType: "claude", AgentName: "coral-go", WorkingDir: t.TempDir(),
	}))
	parent := `{"type":"assistant","message":{"id":"msg_parent","model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":20}},"timestamp":"2026-09-17T02:00:00Z"}` + "\n"
	require.NoError(t, os.WriteFile(env.parentPath, []byte(parent), 0o644))
	require.NoError(t, os.MkdirAll(subagent.Dir(env.parentPath), 0o755))
	return env
}

func (e *subagentEnv) transcriptPath(id string) string {
	return filepath.Join(subagent.Dir(e.parentPath), "agent-"+id+".jsonl")
}

// installFixture copies the realistic fixture transcript and its metadata.
func (e *subagentEnv) installFixture() {
	e.t.Helper()
	for _, suffix := range []string{".jsonl", ".meta.json"} {
		data, err := os.ReadFile(filepath.Join(subFixtureDir, "agent-"+subFixtureID+suffix))
		require.NoError(e.t, err)
		require.NoError(e.t, os.WriteFile(filepath.Join(subagent.Dir(e.parentPath), "agent-"+subFixtureID+suffix), data, 0o644))
	}
}

func subLine(msgID, model string, in, out, cacheRead, cacheWrite int) string {
	return fmt.Sprintf(`{"type":"assistant","agentId":"ignored","sessionId":%q,"timestamp":"2026-09-17T03:00:00Z","message":{"id":%q,"model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`,
		subMainSID, msgID, model, in, out, cacheRead, cacheWrite)
}

func (e *subagentEnv) writeSubagent(id string, lines ...string) {
	e.t.Helper()
	require.NoError(e.t, os.WriteFile(e.transcriptPath(id), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

// appendSubagent adds lines and pushes the mtime forward, since two writes in
// one test can land within the filesystem's timestamp resolution.
func (e *subagentEnv) appendSubagent(id string, lines ...string) {
	e.t.Helper()
	f, err := os.OpenFile(e.transcriptPath(id), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(e.t, err)
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	require.NoError(e.t, err)
	require.NoError(e.t, f.Close())
	future := time.Now().Add(time.Minute)
	require.NoError(e.t, os.Chtimes(e.transcriptPath(id), future, future))
}

func (e *subagentEnv) get(id string) *store.Subagent {
	e.t.Helper()
	sa, err := e.subagents.GetSubagent(context.Background(), subMainSID, id)
	require.NoError(e.t, err)
	return sa
}

func (e *subagentEnv) poller() *TokenPoller {
	p := NewTokenPoller(e.sessions, e.usage, time.Hour)
	p.SetSubagentStore(e.subagents)
	p.sessionPaths[subMainSID] = e.parentPath
	return p
}

func (e *subagentEnv) poll(p *TokenPoller) {
	e.t.Helper()
	ls, err := e.sessions.GetLiveSession(context.Background(), subMainSID)
	require.NoError(e.t, err)
	require.NotNil(e.t, ls)
	p.pollClaudeSession(context.Background(), ls)
}

// ── syncSubagentSpend: files on disk → priced rows ───────────

func TestSyncSubagentSpend_ParsesPricesAndStoresFixture(t *testing.T) {
	env := newSubagentEnv(t)
	env.installFixture()

	n, err := syncSubagentSpend(context.Background(), env.subagents, subMainSID, env.parentPath, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	sa := env.get(subFixtureID)
	require.NotNil(t, sa)
	assert.Equal(t, subMainSID, sa.SessionID, "tied back to the main agent")
	require.NotNil(t, sa.TranscriptPath)
	assert.Equal(t, env.transcriptPath(subFixtureID), *sa.TranscriptPath, "recorded so the detail view can read the conversation")
	assert.Equal(t, "Explore", *sa.SubagentType)
	assert.Equal(t, "Map Coral task-board UI code", *sa.Description)
	assert.Equal(t, "toolu_016f9gxoF7HsvZXEdWAyP9cR", *sa.ToolUseID)
	assert.Equal(t, "claude-opus-5", *sa.Model)
	assert.Equal(t, 1, sa.SpawnDepth)
	assert.Equal(t, 2, sa.APICalls, "msg_01AAA appears three times in the file but is one request")
	assert.Equal(t, 5, sa.InputTokens)
	assert.Equal(t, 600, sa.OutputTokens)
	assert.Equal(t, 26820, sa.CacheReadTokens)
	assert.Equal(t, 7304, sa.CacheWriteTokens)
	assert.Equal(t, "2026-09-17T02:10:18.133Z", *sa.StartedAt)
	assert.Equal(t, "2026-09-17T02:14:11.667Z", *sa.LastActivityAt)

	// claude-opus-5 is priced at $15 in / $75 out / $1.50 cache read /
	// $18.75 cache write per million tokens:
	//   5*15 + 600*75 + 26820*1.5 + 7304*18.75 = 222255 micro-dollars
	assert.InDelta(t, 0.222255, sa.CostUSD, 1e-9)
}

func TestSyncSubagentSpend_MultipleSubagentsAndMissingMetadata(t *testing.T) {
	env := newSubagentEnv(t)
	env.installFixture()
	env.writeSubagent("nometa", subLine("msg_1", "claude-opus-5", 1, 100, 0, 0))

	n, err := syncSubagentSpend(context.Background(), env.subagents, subMainSID, env.parentPath, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	bare := env.get("nometa")
	require.NotNil(t, bare, "spend is tracked even though Claude wrote no metadata file")
	assert.Equal(t, 100, bare.OutputTokens)
	assert.Nil(t, bare.Description)
	assert.Nil(t, bare.SubagentType)
	assert.Equal(t, "nometa", bare.SubagentID, "ID comes from the file name, not the record's agentId")

	spend, err := env.subagents.GetSubagentSpend(context.Background(), []string{subMainSID})
	require.NoError(t, err)
	assert.Equal(t, 2, spend[subMainSID].Subagents)
	assert.Equal(t, 700, spend[subMainSID].OutputTokens)
}

func TestSyncSubagentSpend_IsIdempotentAndTracksGrowth(t *testing.T) {
	env := newSubagentEnv(t)
	ctx := context.Background()
	env.writeSubagent("s1", subLine("msg_1", "claude-opus-5", 1, 100, 1000, 0))

	for i := 0; i < 3; i++ {
		_, err := syncSubagentSpend(ctx, env.subagents, subMainSID, env.parentPath, nil)
		require.NoError(t, err)
	}
	first := env.get("s1")
	assert.Equal(t, 1, first.APICalls)
	assert.Equal(t, 100, first.OutputTokens, "three syncs of an unchanged file must not triple the spend")

	env.appendSubagent("s1",
		subLine("msg_1", "claude-opus-5", 1, 250, 1000, 0), // msg_1 finished streaming
		subLine("msg_2", "claude-opus-5", 1, 40, 2000, 0))
	_, err := syncSubagentSpend(ctx, env.subagents, subMainSID, env.parentPath, nil)
	require.NoError(t, err)

	grown := env.get("s1")
	assert.Equal(t, 2, grown.APICalls)
	assert.Equal(t, 290, grown.OutputTokens)
	assert.Equal(t, 3000, grown.CacheReadTokens)
	assert.Greater(t, grown.CostUSD, first.CostUSD)
	assert.Equal(t, first.ID, grown.ID, "same row")
}

func TestSyncSubagentSpend_PricesEachCallWithItsOwnModel(t *testing.T) {
	env := newSubagentEnv(t)
	env.writeSubagent("mixed",
		subLine("msg_1", "claude-opus-5", 0, 1_000_000, 0, 0),             // $75.00
		subLine("msg_2", "claude-haiku-4-5-20251001", 0, 1_000_000, 0, 0)) // haiku output price
	_, err := syncSubagentSpend(context.Background(), env.subagents, subMainSID, env.parentPath, nil)
	require.NoError(t, err)

	sa := env.get("mixed")
	haikuOnly := sa.CostUSD - 75.0
	assert.Greater(t, haikuOnly, 0.0, "the haiku call is priced too")
	assert.Less(t, haikuOnly, 75.0, "...and not at the opus rate")
	assert.Equal(t, "claude-opus-5", *sa.Model, "first-seen model wins a tie on output")
}

func TestSyncSubagentSpend_UnknownModelStoresTokensWithZeroCost(t *testing.T) {
	env := newSubagentEnv(t)
	env.writeSubagent("s1", subLine("msg_1", "totally-unknown-model-x", 10, 500, 0, 0))
	_, err := syncSubagentSpend(context.Background(), env.subagents, subMainSID, env.parentPath, nil)
	require.NoError(t, err)

	sa := env.get("s1")
	assert.Equal(t, 500, sa.OutputTokens, "tokens are facts and are kept")
	assert.Zero(t, sa.CostUSD, "an unpriceable model yields no cost rather than a guessed one")
}

func TestSyncSubagentSpend_SessionWithoutSubagents(t *testing.T) {
	env := newSubagentEnv(t)
	require.NoError(t, os.RemoveAll(subagent.Dir(env.parentPath)))
	n, err := syncSubagentSpend(context.Background(), env.subagents, subMainSID, env.parentPath, nil)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestSyncSubagentSpend_OneBadSubagentDoesNotBlockOthers(t *testing.T) {
	env := newSubagentEnv(t)
	env.writeSubagent("good", subLine("msg_1", "claude-opus-5", 1, 100, 0, 0))
	// Discovered as a transcript, but unreadable as a file.
	require.NoError(t, os.Mkdir(filepath.Join(subagent.Dir(env.parentPath), "unreadable"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(subagent.Dir(env.parentPath), "unreadable"), env.transcriptPath("bad")))

	n, err := syncSubagentSpend(context.Background(), env.subagents, subMainSID, env.parentPath, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad")
	assert.Equal(t, 1, n)
	assert.NotNil(t, env.get("good"))
	assert.Nil(t, env.get("bad"))
}

func TestSyncSubagentSpend_UnknownMainAgentIsRejected(t *testing.T) {
	env := newSubagentEnv(t)
	env.writeSubagent("s1", subLine("msg_1", "claude-opus-5", 1, 100, 0, 0))
	n, err := syncSubagentSpend(context.Background(), env.subagents, "99999999-0000-0000-0000-000000000000", env.parentPath, nil)
	require.Error(t, err, "a subagent cannot be stored without a main agent to belong to")
	assert.Zero(t, n)
}

// ── Token poller integration ─────────────────────────────────

func TestPollClaudeSession_RecordsSubagentSpendSeparatelyFromMainAgent(t *testing.T) {
	env := newSubagentEnv(t)
	env.installFixture()
	env.poll(env.poller())

	sa := env.get(subFixtureID)
	require.NotNil(t, sa)
	assert.Equal(t, 600, sa.OutputTokens)

	// The main agent's own usage holds only its own transcript's call.
	var mainRows, mainOutput int
	require.NoError(t, env.db.GetContext(context.Background(), &mainRows,
		"SELECT COUNT(*) FROM token_usage WHERE session_id = ?", subMainSID))
	require.NoError(t, env.db.GetContext(context.Background(), &mainOutput,
		"SELECT COALESCE(SUM(output_tokens), 0) FROM token_usage WHERE session_id = ?", subMainSID))
	assert.Equal(t, 1, mainRows)
	assert.Equal(t, 20, mainOutput, "subagent tokens are not mixed into the main agent's usage")
}

// Subagents run in the background while the main agent waits on them, so the
// main transcript can sit unchanged for minutes while subagent spend grows.
// The main-transcript mtime check must not short-circuit subagent polling.
func TestPollClaudeSession_SubagentSpendUpdatesWhileMainTranscriptIsIdle(t *testing.T) {
	env := newSubagentEnv(t)
	env.writeSubagent("s1", subLine("msg_1", "claude-opus-5", 1, 100, 0, 0))
	p := env.poller()

	env.poll(p)
	assert.Equal(t, 100, env.get("s1").OutputTokens)

	env.appendSubagent("s1", subLine("msg_2", "claude-opus-5", 1, 900, 0, 0)) // parent file untouched
	env.poll(p)
	assert.Equal(t, 1000, env.get("s1").OutputTokens)
}

func TestPollClaudeSession_PicksUpSubagentLaunchedLater(t *testing.T) {
	env := newSubagentEnv(t)
	p := env.poller()
	env.poll(p)
	assert.Nil(t, env.get("late"))

	env.writeSubagent("late", subLine("msg_1", "claude-opus-5", 1, 55, 0, 0))
	env.poll(p)
	require.NotNil(t, env.get("late"))
	assert.Equal(t, 55, env.get("late").OutputTokens)
}

func TestPollClaudeSession_SkipsUnchangedSubagentFiles(t *testing.T) {
	env := newSubagentEnv(t)
	env.writeSubagent("s1", subLine("msg_1", "claude-opus-5", 1, 100, 0, 0))
	p := env.poller()
	env.poll(p)

	// Plant a sentinel. If the unchanged file were re-read, it would be overwritten.
	_, err := env.db.ExecContext(context.Background(),
		"UPDATE subagents SET output_tokens = 424242 WHERE subagent_id = 's1'")
	require.NoError(t, err)
	env.poll(p)
	assert.Equal(t, 424242, env.get("s1").OutputTokens, "unchanged transcript must not be re-parsed")

	env.appendSubagent("s1", subLine("msg_2", "claude-opus-5", 1, 1, 0, 0))
	env.poll(p)
	assert.Equal(t, 101, env.get("s1").OutputTokens, "a changed transcript is re-read in full")
}

// Claude can write the metadata file after the transcript already exists.
func TestPollClaudeSession_LateMetadataIsPickedUp(t *testing.T) {
	env := newSubagentEnv(t)
	env.writeSubagent("s1", subLine("msg_1", "claude-opus-5", 1, 100, 0, 0))
	p := env.poller()
	env.poll(p)
	require.Nil(t, env.get("s1").Description)

	metaPath := filepath.Join(subagent.Dir(env.parentPath), "agent-s1.meta.json")
	require.NoError(t, os.WriteFile(metaPath, []byte(`{"agentType":"Plan","description":"Design the thing","toolUseId":"toolu_9"}`), 0o644))
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(metaPath, future, future))
	env.poll(p)

	sa := env.get("s1")
	require.NotNil(t, sa.Description)
	assert.Equal(t, "Design the thing", *sa.Description)
	assert.Equal(t, "Plan", *sa.SubagentType)
}

func TestPollClaudeSession_WithoutSubagentStoreIsANoOp(t *testing.T) {
	env := newSubagentEnv(t)
	env.installFixture()
	p := NewTokenPoller(env.sessions, env.usage, time.Hour) // SetSubagentStore not called
	p.sessionPaths[subMainSID] = env.parentPath

	require.NotPanics(t, func() { env.poll(p) })
	assert.Nil(t, env.get(subFixtureID))
}

func TestSyncSubagentSpend_TracksFinishedState(t *testing.T) {
	env := newSubagentEnv(t)
	ctx := context.Background()
	stop := func(msgID, reason string) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-09-17T03:00:00Z","message":{"id":%q,"model":"claude-opus-5","stop_reason":%q,"usage":{"input_tokens":1,"output_tokens":10}}}`, msgID, reason)
	}
	env.writeSubagent("s1", stop("msg_1", "tool_use"))
	_, err := syncSubagentSpend(ctx, env.subagents, subMainSID, env.parentPath, nil)
	require.NoError(t, err)
	assert.False(t, env.get("s1").Finished, "mid tool call")

	env.appendSubagent("s1", stop("msg_2", "end_turn"))
	_, err = syncSubagentSpend(ctx, env.subagents, subMainSID, env.parentPath, nil)
	require.NoError(t, err)
	assert.True(t, env.get("s1").Finished)

	env.installFixture()
	_, err = syncSubagentSpend(ctx, env.subagents, subMainSID, env.parentPath, nil)
	require.NoError(t, err)
	assert.True(t, env.get(subFixtureID).Finished, "the realistic fixture ends with end_turn")
}
