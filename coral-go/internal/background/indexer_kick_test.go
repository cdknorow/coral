package background

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
)

// #6198: a new Codex conversation was listed in History only at the next index pass, up to the whole interval (two minutes) after its reply. A Kick brings the pass forward to seconds.
// A real CodexAgent scanner over a CODEX_HOME in a temp dir: the indexer is already running (its startup pass found nothing), then Codex writes its first rollout, and the conversation is listed
// without a restart and without waiting for the interval.
func TestAKickedIndexerListsANewCodexConversationWithoutWaitingForTheInterval(t *testing.T) {
	prevDelay, prevGap := kickDelay, kickGap
	kickDelay, kickGap = 50*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { kickDelay, kickGap = prevDelay, prevGap })

	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	idx := NewSessionIndexer(ss, []agent.HistoryScanner{&agent.CodexAgent{}}, time.Hour, 0)
	done := make(chan struct{})
	go func() { defer close(done); idx.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	listed := func() int {
		res, err := ss.ListSessionsPaged(context.Background(), store.SessionListParams{Page: 1, PageSize: 50})
		require.NoError(t, err)
		return res.Total
	}
	time.Sleep(300 * time.Millisecond) // sleep-ok: the startup pass has run over an empty CODEX_HOME; there is no event to wait for
	require.Equal(t, 0, listed())

	day := filepath.Join(home, "sessions", "2026", "10", "22")
	require.NoError(t, os.MkdirAll(day, 0o755))
	rollout := `{"timestamp":"2026-10-22T04:37:22.293Z","type":"session_meta","payload":{"id":"019e90eb-a08a-7511-a410-23e7ae3e62a8"}}
{"timestamp":"2026-10-22T04:37:22.296Z","type":"event_msg","payload":{"type":"user_message","message":"which folder are you in?","images":[]}}
{"timestamp":"2026-10-22T04:37:28.017Z","type":"event_msg","payload":{"type":"agent_message","message":"I am in the project folder.","phase":"final_answer"}}
`
	require.NoError(t, os.WriteFile(filepath.Join(day, "rollout-2026-10-22T04-37-22-019e90eb-a08a-7511-a410-23e7ae3e62a8.jsonl"), []byte(rollout), 0o644))
	time.Sleep(300 * time.Millisecond) // sleep-ok: nothing kicks the indexer: it stays unlisted until its interval, which is an hour here
	require.Equal(t, 0, listed(), "without a kick the new conversation waits for the interval")

	idx.Kick()
	require.Eventually(t, func() bool { return listed() == 1 }, 5*time.Second, 25*time.Millisecond, "a kick lists the new conversation within seconds")
}

// Kicks coalesce, and passes started by kicks are at least kickGap apart: a busy agent does not have the indexer walking its folders on every reply.
func TestKicksCoalesceAndPassesAreSpacedByTheGap(t *testing.T) {
	prevDelay, prevGap := kickDelay, kickGap
	kickDelay, kickGap = 20*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { kickDelay, kickGap = prevDelay, prevGap })

	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	fs := &countingScanner{}
	idx := NewSessionIndexer(store.NewSessionStore(db), []agent.HistoryScanner{fs}, time.Hour, 0)
	done := make(chan struct{})
	go func() { defer close(done); idx.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return fs.passes() == 1 }, 3*time.Second, 10*time.Millisecond) // the startup pass

	for i := 0; i < 50; i++ {
		idx.Kick()
	}
	require.Eventually(t, func() bool { return fs.passes() == 2 }, 3*time.Second, 10*time.Millisecond, "fifty kicks make one pass")
	for i := 0; i < 50; i++ {
		idx.Kick()
	}
	time.Sleep(150 * time.Millisecond) // sleep-ok: inside the gap nothing may start
	require.Equal(t, 2, fs.passes(), "a pass is not started within kickGap of the last")
	require.Eventually(t, func() bool { return fs.passes() == 3 }, 3*time.Second, 10*time.Millisecond, "the next kicked pass waits out the gap and then runs once")
}

type countingScanner struct {
	mu sync.Mutex
	n  int
}

func (c *countingScanner) HistoryBasePath() string    { return "/fake" }
func (c *countingScanner) HistoryGlobPattern() string { return "*.jsonl" }
func (c *countingScanner) ExtractSessions(string, map[string]float64) ([]agent.IndexedSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return nil, nil
}
func (c *countingScanner) passes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
