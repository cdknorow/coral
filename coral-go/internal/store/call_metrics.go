package store

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/cdknorow/coral/internal/safego"
	setreg "github.com/cdknorow/coral/internal/settings"
)

type CallMetric struct {
	CallType   string `json:"call_type"`
	Operation  string `json:"operation"`
	AgentName  string `json:"agent_name,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	TeamID     *int64 `json:"team_id,omitempty"`
	BoardName  string `json:"board_name,omitempty"`
	Method     string `json:"method,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	IsError    bool   `json:"is_error"`
	CreatedAt  string `json:"created_at"`

	// FallbackAgentName is stored as agent_name when SessionID does not
	// resolve to a live session and AgentName is empty (for example a chi
	// route parameter). Only queued metrics use it.
	FallbackAgentName string `json:"-"`
}

func (s *DB) RecordCallMetric(ctx context.Context, m *CallMetric) error {
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.ExecContext(ctx, `INSERT INTO call_metrics(call_type,operation,agent_name,session_id,team_id,board_name,method,status_code,duration_ms,is_error,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, m.CallType, m.Operation, m.AgentName, m.SessionID, m.TeamID, m.BoardName, m.Method, m.StatusCode, m.DurationMs, m.IsError, m.CreatedAt)
	return err
}

// Request metrics are buffered and written in batches. Recording one row per
// /api request synchronously cost a live_sessions lookup and an INSERT on
// SQLite's single connection for every request, so each API call queued twice
// behind real work.
var (
	callMetricFlushInterval = 10 * time.Second
	callMetricFlushBatch    = 500    // pending rows that trigger an early flush
	callMetricMaxPending    = 20_000 // beyond this, new metrics are dropped
	// callMetricMaxRows bounds the raw rows (#5359): the summary reads them all, at ~2 us a row,
	// and the roll-up only moves rows older than callMetricRawHours. Past it new metrics are not
	// written (counted and logged, once per run of drops) until the roll-up makes room; rows
	// already stored are never touched.
	callMetricMaxRows int64 = 500_000
)

type callMetricBuffer struct {
	mu      sync.Mutex
	pending []*CallMetric
	dropped int64
	closed  bool

	capDropped atomic.Int64 // metrics not written because the table is at callMetricMaxRows (#5359)
	capLogged  atomic.Bool

	flushMu sync.Mutex // serializes flushes so rows are written in order
	kick    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	start   sync.Once
	close   sync.Once
}

func (d *DB) callMetricBuf() *callMetricBuffer {
	d.callMetricsInit.Do(func() {
		d.callMetrics = &callMetricBuffer{
			kick: make(chan struct{}, 1),
			stop: make(chan struct{}),
			done: make(chan struct{}),
		}
	})
	return d.callMetrics
}

// EnqueueCallMetric queues a request metric for the next batched write. It
// never blocks on the database. A metric with a SessionID has its agent name
// resolved to the live session's display name when the batch is written.
func (d *DB) EnqueueCallMetric(m *CallMetric) {
	if m.CreatedAt == "" {
		m.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b := d.callMetricBuf()
	b.mu.Lock()
	if b.closed || len(b.pending) >= callMetricMaxPending {
		b.dropped++
		b.mu.Unlock()
		return
	}
	// Kick an early flush only when this metric crosses the threshold. After
	// a failed flush re-queues a full batch, the queue stays above it, so a
	// persistent DB error is retried on the ticker rather than per request.
	crossed := len(b.pending) < callMetricFlushBatch
	b.pending = append(b.pending, m)
	full := crossed && len(b.pending) >= callMetricFlushBatch
	b.mu.Unlock()

	b.start.Do(func() { safego.Restart("store/call_metrics:d.callMetricLoop", func() { d.callMetricLoop(b) }) })
	if full {
		select {
		case b.kick <- struct{}{}:
		default:
		}
	}
}

func (d *DB) callMetricLoop(b *callMetricBuffer) {
	defer close(b.done)
	t := time.NewTicker(callMetricFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-b.kick:
		case <-b.stop:
			d.finalCallMetricFlush()
			return
		}
		d.logCallMetricFlush(d.FlushCallMetrics(context.Background()))
	}
}

// callMetricCloseTimeout bounds the flush at shutdown (#4299): a write connection held
// elsewhere must not hold up Close for SQLite's 30 s busy timeout, or longer.
var callMetricCloseTimeout = 5 * time.Second

// finalCallMetricFlush writes what is queued when the DB closes, within callMetricCloseTimeout.
func (d *DB) finalCallMetricFlush() {
	ctx, cancel := context.WithTimeout(context.Background(), callMetricCloseTimeout)
	defer cancel()
	d.logCallMetricFlush(d.FlushCallMetrics(ctx))
}

func (d *DB) logCallMetricFlush(err error) {
	if err != nil {
		log.Printf("[call-metrics] flush failed: %v", err)
	}
}

// FlushCallMetrics writes every queued metric in one transaction.
func (d *DB) FlushCallMetrics(ctx context.Context) error {
	b := d.callMetricBuf()
	b.flushMu.Lock()
	defer b.flushMu.Unlock()

	b.mu.Lock()
	batch := b.pending
	b.pending = nil
	dropped := b.dropped
	b.dropped = 0
	b.mu.Unlock()
	if dropped > 0 {
		log.Printf("[call-metrics] dropped %d metrics: queue full", dropped)
	}
	if len(batch) == 0 {
		return nil
	}

	if err := d.writeCallMetrics(ctx, batch); err != nil {
		// Put the batch back for the next flush (oldest first, within the
		// queue bound) unless the DB is closing, so a transient error such as
		// SQLITE_BUSY does not lose it.
		b.mu.Lock()
		lost, queued := 0, 0
		if b.closed {
			lost = len(batch)
		} else {
			merged := append(batch, b.pending...)
			if len(merged) > callMetricMaxPending {
				lost = len(merged) - callMetricMaxPending
				merged = merged[:callMetricMaxPending]
			}
			b.pending = merged
			queued = len(merged)
		}
		b.mu.Unlock()
		return fmt.Errorf("write %d call metrics: %w (%d dropped, %d queued for retry)", len(batch), err, lost, queued)
	}
	return nil
}

// writeCallMetrics inserts batch in one transaction.
func (d *DB) writeCallMetrics(ctx context.Context, batch []*CallMetric) error {
	names, err := d.liveDisplayNames(ctx, batch)
	if err != nil {
		return err
	}
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	batch, err = d.trimToRowCap(ctx, tx, batch)
	if err != nil || len(batch) == 0 {
		return err
	}
	stmt, err := tx.PreparexContext(ctx, `INSERT INTO call_metrics(call_type,operation,agent_name,session_id,team_id,board_name,method,status_code,duration_ms,is_error,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, m := range batch {
		if name, ok := names[m.SessionID]; ok && m.SessionID != "" {
			m.AgentName = name
		}
		if m.AgentName == "" {
			m.AgentName = m.FallbackAgentName
		}
		if _, err := stmt.ExecContext(ctx, m.CallType, m.Operation, m.AgentName, m.SessionID, m.TeamID, m.BoardName, m.Method, m.StatusCode, m.DurationMs, m.IsError, m.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// trimToRowCap keeps as many of batch as fit under callMetricMaxRows. The stored count is the
// span of ids (MAX-MIN+1, two index probes, not a COUNT scan): the roll-up deletes whole hours
// from the old end, so the span stays close to the count and errs toward dropping early.
func (d *DB) trimToRowCap(ctx context.Context, tx *sqlx.Tx, batch []*CallMetric) ([]*CallMetric, error) {
	var span int64
	if err := tx.GetContext(ctx, &span, `SELECT COALESCE(MAX(id) - MIN(id) + 1, 0) FROM call_metrics`); err != nil {
		return nil, err
	}
	room := callMetricMaxRows - span
	if room >= int64(len(batch)) {
		d.callMetricBuf().capLogged.Store(false)
		return batch, nil
	}
	room = max(room, 0)
	lost := int64(len(batch)) - room
	b := d.callMetricBuf()
	b.capDropped.Add(lost)
	if b.capLogged.CompareAndSwap(false, true) {
		log.Printf("[call-metrics] %d raw rows stored (cap %d): new metrics are not written until the roll-up makes room", span, callMetricMaxRows)
	}
	return batch[:int(room)], nil
}

// CallMetricsCapDropped is how many metrics were not written because of callMetricMaxRows.
func (d *DB) CallMetricsCapDropped() int64 { return d.callMetricBuf().capDropped.Load() }

// liveDisplayNames maps each distinct session ID in batch to its live
// session's display name (or agent name), with one query per 500 IDs.
func (d *DB) liveDisplayNames(ctx context.Context, batch []*CallMetric) (map[string]string, error) {
	seen := map[string]bool{}
	var ids []string
	for _, m := range batch {
		if m.SessionID != "" && !seen[m.SessionID] {
			seen[m.SessionID] = true
			ids = append(ids, m.SessionID)
		}
	}
	names := make(map[string]string, len(ids))
	for len(ids) > 0 {
		chunk := ids[:min(len(ids), 500)]
		ids = ids[len(chunk):]
		q, args, err := sqlx.In(`SELECT session_id, COALESCE(NULLIF(display_name, ''), agent_name) FROM live_sessions WHERE session_id IN (?)`, chunk)
		if err != nil {
			return nil, err
		}
		rows, err := d.QueryContext(ctx, d.Rebind(q), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var name *string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return nil, err
			}
			if name != nil {
				names[id] = *name
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// closeCallMetrics stops the flusher after writing what is still queued.
func (d *DB) closeCallMetrics() {
	b := d.callMetricBuf()
	b.close.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		started := true
		b.start.Do(func() { started = false })
		if !started {
			d.finalCallMetricFlush()
			return
		}
		close(b.stop)
		<-b.done
	})
}

type CallMetricSummary struct {
	Operation     string  `db:"operation" json:"operation"`
	CallType      string  `db:"call_type" json:"call_type"`
	AgentName     string  `db:"agent_name" json:"agent_name"`
	BoardName     string  `db:"board_name" json:"board_name"`
	Calls         int     `db:"calls" json:"calls"`
	Errors        int     `db:"errors" json:"errors"`
	AvgDurationMs float64 `db:"avg_duration_ms" json:"avg_duration_ms"`
}

// internalMetricOperations are the dashboard's and hub's own plumbing routes:
// liveness polls, usage beacons and the dashboard's status poll. They are not
// tool or API calls anyone made, so they are neither recorded nor shown in the
// call summary (#3152).
var internalMetricOperations = []string{"/api/health", "/api/tracking/event", "/api/system/status"}

// IsInternalMetricOperation reports whether a route pattern is plumbing (see
// internalMetricOperations).
func IsInternalMetricOperation(op string) bool {
	for _, o := range internalMetricOperations {
		if op == o {
			return true
		}
	}
	return false
}

// IsInternalRemotePath reports whether a request the hub proxies to a remote
// (/api/remote/{server}/<path>) is plumbing: the remote's health, status and
// agent-list polls. Proxied user actions (launch, send, …) are calls like any
// other and are kept (#3152).
func IsInternalRemotePath(method, path string) bool {
	if IsInternalMetricOperation(path) {
		return true
	}
	return method == "GET" && path == "/api/sessions/live"
}

// CallMetricSummary first writes queued metrics so the summary includes
// every request recorded so far.
func (s *DB) CallMetricSummary(ctx context.Context, since time.Time) ([]CallMetricSummary, error) {
	if err := s.FlushCallMetrics(ctx); err != nil {
		return nil, err
	}
	// Raw rows for the last day, hourly rollups before that (#4098): no hour is in both, since
	// an hour's raw rows are deleted in the transaction that rolls them up. A window starting
	// mid-hour counts that whole rolled-up hour.
	var out []CallMetricSummary
	err := s.Reader().SelectContext(ctx, &out, `SELECT operation, call_type, agent_name, board_name,
		SUM(calls) AS calls, SUM(errors) AS errors,
		CASE WHEN SUM(duration_n) > 0 THEN CAST(SUM(duration_sum_ms) AS REAL) / SUM(duration_n) ELSE 0 END AS avg_duration_ms
		FROM (
			SELECT cm.operation, cm.call_type,
				COALESCE(NULLIF(ls.display_name, ''), cm.agent_name, '') AS agent_name,
				COALESCE(NULLIF(cm.board_name, ''), ls.board_name, '') AS board_name,
				1 AS calls, cm.is_error AS errors,
				COALESCE(cm.duration_ms, 0) AS duration_sum_ms,
				CASE WHEN cm.duration_ms IS NULL THEN 0 ELSE 1 END AS duration_n
			FROM call_metrics cm LEFT JOIN live_sessions ls ON ls.session_id = cm.session_id
			WHERE cm.created_at >= ? AND cm.operation NOT IN (?, ?, ?)
			UNION ALL
			SELECT h.operation, h.call_type,
				COALESCE(NULLIF(ls.display_name, ''), h.agent_name) AS agent_name,
				COALESCE(NULLIF(h.board_name, ''), ls.board_name, '') AS board_name,
				h.calls, h.errors, h.duration_sum_ms, h.duration_n
			FROM call_metrics_hourly h LEFT JOIN live_sessions ls ON ls.session_id = NULLIF(h.session_id, '')
			WHERE h.hour >= ? AND h.operation NOT IN (?, ?, ?)
		)
		GROUP BY operation, call_type, agent_name, board_name
		ORDER BY SUM(calls) DESC`,
		since.UTC().Format(time.RFC3339Nano), internalMetricOperations[0], internalMetricOperations[1], internalMetricOperations[2],
		hourPrefix(since), internalMetricOperations[0], internalMetricOperations[1], internalMetricOperations[2])
	return out, err
}

// Retention (#3261). Raw call metrics are kept for CallMetricsRetentionDays
// (the call_metrics_retention_days setting); the summary never reads further
// back than that.
const (
	CallMetricsRetentionSetting     = setreg.CallMetricsRetentionDay
	CallMetricsRetentionDefaultDays = 14
)

// CallMetricsRetentionDays reads the retention setting: the default when it is
// missing or not a number, clamped to 1-90 days.
func CallMetricsRetentionDays(settings map[string]string) int {
	return int(setreg.Int(settings, CallMetricsRetentionSetting))
}

// PruneCallMetrics deletes at most batch call metrics recorded before cutoff,
// oldest first, in one short statement, and reports how many it deleted. It
// walks idx_call_metrics_created (created_at is RFC 3339 UTC, so string order
// is time order) and never sorts the whole history; a caller that wants
// everything gone calls it again, yielding the single connection in between.
func (d *DB) PruneCallMetrics(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM call_metrics WHERE id IN (
		SELECT id FROM call_metrics WHERE created_at < ? ORDER BY created_at LIMIT ?)`,
		cutoff.UTC().Format(time.RFC3339Nano), batch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
