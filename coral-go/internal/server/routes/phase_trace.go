package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/cdknorow/coral/internal/store"
)

var phaseTraceSeq atomic.Uint64

// phaseTrace is opt-in request instrumentation for diagnosing startup stalls.
// It records phase boundaries and cumulative elapsed time without transcript,
// prompt, SQL, or message contents.
type phaseTrace struct {
	enabled bool
	id      string
	start   time.Time
	last    time.Time
}

// dbPoolAttrs reports database/sql cumulative pool counters. They describe
// shared contention across requests, not time charged to an individual phase.
func dbPoolAttrs(db *store.DB) []any {
	if db == nil {
		return nil
	}
	stats := db.Stats()
	return []any{"db_in_use", stats.InUse, "db_idle", stats.Idle, "db_wait_count_total", stats.WaitCount, "db_wait_ms_total", stats.WaitDuration.Milliseconds()}
}

func newPhaseTrace(kind string) *phaseTrace {
	if os.Getenv("CORAL_DEBUG_PHASES") != "1" {
		return &phaseTrace{}
	}
	now := time.Now()
	return &phaseTrace{enabled: true, id: fmt.Sprintf("%s-%d", kind, phaseTraceSeq.Add(1)), start: now, last: now}
}

func (t *phaseTrace) begin(phase string) {
	if !t.enabled {
		return
	}
	now := time.Now()
	slog.Info("request phase start", "trace_id", t.id, "phase", phase, "elapsed_ms", now.Sub(t.start).Milliseconds(), "build", os.Getenv("CORAL_BUILD_ID"))
	t.last = now
}

func (t *phaseTrace) end(phase string, attrs ...any) {
	if !t.enabled {
		return
	}
	now := time.Now()
	args := []any{"trace_id", t.id, "phase", phase, "phase_ms", now.Sub(t.last).Milliseconds(), "elapsed_ms", now.Sub(t.start).Milliseconds()}
	args = append(args, attrs...)
	slog.Info("request phase complete", args...)
	t.last = now
}

func (t *phaseTrace) finish(attrs ...any) {
	if !t.enabled {
		return
	}
	now := time.Now()
	args := []any{"trace_id", t.id, "total_ms", now.Sub(t.start).Milliseconds()}
	args = append(args, attrs...)
	slog.Info("request phase summary", args...)
}

func (t *phaseTrace) agentDetail(phase, sessionID string, duration time.Duration) {
	if !t.enabled {
		return
	}
	hash := sha256.Sum256([]byte(sessionID))
	slog.Info("request enrichment detail", "trace_id", t.id, "phase", phase, "agent", hex.EncodeToString(hash[:4]), "duration_ms", duration.Milliseconds())
}
