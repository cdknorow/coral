package store

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func countCallMetrics(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.Get(&n, `SELECT COUNT(*) FROM call_metrics`))
	return n
}

func setCallMetricLimits(t *testing.T, interval time.Duration, batch, max int) {
	t.Helper()
	oi, ob, om := callMetricFlushInterval, callMetricFlushBatch, callMetricMaxPending
	callMetricFlushInterval, callMetricFlushBatch, callMetricMaxPending = interval, batch, max
	t.Cleanup(func() { callMetricFlushInterval, callMetricFlushBatch, callMetricMaxPending = oi, ob, om })
}

func insertLiveSession(t *testing.T, db *DB, id, agent, display string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO live_sessions(session_id, agent_type, agent_name, display_name, working_dir, created_at) VALUES(?,?,?,?,?,?)`,
		id, "claude", agent, display, "/tmp", time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
}

// Enqueue does no database work; the summary still sees every queued request.
func TestEnqueueCallMetricIsDeferredButVisibleToSummary(t *testing.T) {
	setCallMetricLimits(t, time.Hour, 1000, 1000)
	db := openTestDB(t)
	for i := 0; i < 3; i++ {
		db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/x", Method: "GET", StatusCode: 200})
	}
	assert.Equal(t, 0, countCallMetrics(t, db), "nothing is written per request")

	rows, err := db.CallMetricSummary(context.Background(), time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 3, rows[0].Calls)
	assert.Equal(t, 3, countCallMetrics(t, db))
}

// Agent names resolve exactly as the old per-request lookup did: a live
// session's display name (or agent name) wins, then the supplied name, then
// the route-parameter fallback.
func TestFlushCallMetricsResolvesAgentNames(t *testing.T) {
	setCallMetricLimits(t, time.Hour, 1000, 1000)
	db := openTestDB(t)
	insertLiveSession(t, db, "s-display", "agent_a", "Lead Developer")
	insertLiveSession(t, db, "s-nodisplay", "agent_b", "")

	cases := []struct {
		m    CallMetric
		want string
	}{
		{CallMetric{Operation: "/a", AgentName: "Header Name", SessionID: "s-display"}, "Lead Developer"},
		{CallMetric{Operation: "/b", SessionID: "s-nodisplay", FallbackAgentName: "route"}, "agent_b"},
		{CallMetric{Operation: "/c", AgentName: "Header Name", SessionID: "gone"}, "Header Name"},
		{CallMetric{Operation: "/d", SessionID: "gone", FallbackAgentName: "route"}, "route"},
		{CallMetric{Operation: "/e", AgentName: "Header Name", FallbackAgentName: "route"}, "Header Name"},
		{CallMetric{Operation: "/f"}, ""},
	}
	for i := range cases {
		m := cases[i].m
		m.CallType = "api"
		db.EnqueueCallMetric(&m)
	}
	require.NoError(t, db.FlushCallMetrics(context.Background()))

	got := map[string]string{}
	rows, err := db.Query(`SELECT operation, COALESCE(agent_name, '') FROM call_metrics`)
	require.NoError(t, err)
	for rows.Next() {
		var op, name string
		require.NoError(t, rows.Scan(&op, &name))
		got[op] = name
	}
	require.NoError(t, rows.Close())
	for _, c := range cases {
		assert.Equal(t, c.want, got[c.m.Operation], c.m.Operation)
	}
}

func TestCallMetricsFlushOnBatchSizeAndInterval(t *testing.T) {
	t.Run("batch", func(t *testing.T) {
		setCallMetricLimits(t, time.Hour, 5, 1000)
		db := openTestDB(t)
		for i := 0; i < 5; i++ {
			db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/x"})
		}
		require.Eventually(t, func() bool { return countCallMetrics(t, db) == 5 }, 3*time.Second, 5*time.Millisecond)
	})
	t.Run("interval", func(t *testing.T) {
		setCallMetricLimits(t, 20*time.Millisecond, 1000, 1000)
		db := openTestDB(t)
		db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/x"})
		require.Eventually(t, func() bool { return countCallMetrics(t, db) == 1 }, 3*time.Second, 5*time.Millisecond)
	})
}

// Shutdown must not lose the last interval of metrics.
func TestCloseFlushesQueuedCallMetrics(t *testing.T) {
	setCallMetricLimits(t, time.Hour, 1000, 1000)
	path := filepath.Join(t.TempDir(), "sessions.db")
	for _, started := range []bool{true, false} {
		db, err := OpenWithKey(context.Background(), path, "")
		require.NoError(t, err)
		if started {
			db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/x"})
		}
		db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/y"})
		require.NoError(t, db.Close())
		db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/after-close"}) // dropped, no panic
	}
	db, err := OpenWithKey(context.Background(), path, "")
	require.NoError(t, err)
	defer db.Close()
	assert.Equal(t, 3, countCallMetrics(t, db))
}

// #4299: the flush at shutdown gives up after callMetricCloseTimeout when the write connection
// is held, instead of holding up Close until it is free.
func TestCloseFlushOfCallMetricsHasADeadline(t *testing.T) {
	setCallMetricLimits(t, time.Hour, 1000, 1000)
	old := callMetricCloseTimeout
	callMetricCloseTimeout = 100 * time.Millisecond
	t.Cleanup(func() { callMetricCloseTimeout = old })
	db := openTestDB(t)
	db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/x"})
	tx, err := db.Beginx() // holds the only write connection
	require.NoError(t, err)
	defer tx.Rollback()

	done := make(chan struct{})
	go func() { db.closeCallMetrics(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("closing waited for the write connection past the deadline")
	}
}

func TestCallMetricQueueIsBounded(t *testing.T) {
	setCallMetricLimits(t, time.Hour, 1000, 3)
	db := openTestDB(t)
	for i := 0; i < 10; i++ {
		db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/x"})
	}
	require.NoError(t, db.FlushCallMetrics(context.Background()))
	assert.Equal(t, 3, countCallMetrics(t, db))
}

// Concurrent requests, background flushes and Close lose nothing and race on
// nothing (run with -race).
func TestCallMetricsConcurrentEnqueueFlushAndClose(t *testing.T) {
	setCallMetricLimits(t, time.Millisecond, 7, 100_000)
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := OpenWithKey(context.Background(), path, "")
	require.NoError(t, err)
	insertLiveSession(t, db, "s1", "agent", "Display")

	const workers, each = 8, 250
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: fmt.Sprintf("/api/%d", w), SessionID: "s1"})
				if i%50 == 0 {
					_, _ = db.CallMetricSummary(context.Background(), time.Now().Add(-time.Hour))
				}
			}
		}(w)
	}
	wg.Wait()
	require.NoError(t, db.Close())

	db, err = OpenWithKey(context.Background(), path, "")
	require.NoError(t, err)
	defer db.Close()
	assert.Equal(t, workers*each, countCallMetrics(t, db))
	var wrong int
	require.NoError(t, db.Get(&wrong, `SELECT COUNT(*) FROM call_metrics WHERE agent_name != 'Display'`))
	assert.Zero(t, wrong)
}

// A failed flush keeps its batch for the next one instead of dropping it.
func TestFailedCallMetricFlushRequeuesBatch(t *testing.T) {
	setCallMetricLimits(t, time.Hour, 1000, 1000)
	db := openTestDB(t)
	db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/first"})
	_, err := db.Exec(`ALTER TABLE call_metrics RENAME TO call_metrics_away`)
	require.NoError(t, err)

	err = db.FlushCallMetrics(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "0 dropped, 1 queued for retry")

	db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/second"})
	_, err = db.Exec(`ALTER TABLE call_metrics_away RENAME TO call_metrics`)
	require.NoError(t, err)
	require.NoError(t, db.FlushCallMetrics(context.Background()))

	var ops []string
	require.NoError(t, db.Select(&ops, `SELECT operation FROM call_metrics ORDER BY id`))
	assert.Equal(t, []string{"/api/first", "/api/second"}, ops, "requeued metrics keep their order")
}

// A persistent write error is retried on the ticker, not once per request:
// after a failed flush re-queues a full batch, new metrics must not kick
// another flush.
func TestFailedCallMetricFlushDoesNotRetryPerRequest(t *testing.T) {
	setCallMetricLimits(t, time.Hour, 3, 1000)
	var logs syncBuffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	db := openTestDB(t)
	_, err := db.Exec(`ALTER TABLE call_metrics RENAME TO call_metrics_away`)
	require.NoError(t, err)

	for i := 0; i < 3; i++ { // crosses the threshold: one early flush, which fails
		db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/x"})
	}
	require.Eventually(t, func() bool { return strings.Count(logs.String(), "flush failed") == 1 }, 3*time.Second, 5*time.Millisecond)
	for i := 0; i < 50; i++ {
		db.EnqueueCallMetric(&CallMetric{CallType: "api", Operation: "/api/x"})
	}
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 1, strings.Count(logs.String(), "flush failed"), "no flush per request while the queue stays above the threshold")
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
