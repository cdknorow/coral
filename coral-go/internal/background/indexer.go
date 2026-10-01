package background

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/store"
)

// SessionIndexer scans history files for all registered agents and upserts
// into session_index + session_fts.
type SessionIndexer struct {
	store        *store.SessionStore
	scanners     []agent.HistoryScanner
	interval     time.Duration
	startupDelay time.Duration
	logger       *slog.Logger
}

// NewSessionIndexer creates a new SessionIndexer.
func NewSessionIndexer(sessionStore *store.SessionStore, scanners []agent.HistoryScanner, interval, startupDelay time.Duration) *SessionIndexer {
	return &SessionIndexer{
		store:        sessionStore,
		scanners:     scanners,
		interval:     interval,
		startupDelay: startupDelay,
		logger:       slog.Default().With("service", "session_indexer"),
	}
}

// Run starts the indexing loop. Blocks until context is cancelled.
func (idx *SessionIndexer) Run(ctx context.Context) error {
	phaseLogging := os.Getenv("CORAL_DEBUG_PHASES") == "1"
	if phaseLogging {
		idx.logger.Info("indexer startup delay begin", "startup_delay_ms", idx.startupDelay.Milliseconds())
	}
	// Wait for startup delay
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(idx.startupDelay):
	}
	if phaseLogging {
		idx.logger.Info("indexer startup delay complete")
	}

	ticker := time.NewTicker(idx.interval)
	defer ticker.Stop()

	// Run immediately after startup delay
	if err := idx.RunOnce(ctx); err != nil {
		idx.logger.Error("indexer pass failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := idx.RunOnce(ctx); err != nil {
				idx.logger.Error("indexer pass failed", "error", err)
			}
		}
	}
}

// RunOnce performs a single indexing pass over all registered scanners.
func (idx *SessionIndexer) RunOnce(ctx context.Context) error {
	started := time.Now()
	knownMtimes, err := idx.store.GetIndexedMtimes(ctx)
	if err != nil {
		return err
	}

	var indexed int

	// Collect all sessions from all scanners first
	var allSessions []agent.IndexedSession
	for _, scanner := range idx.scanners {
		basePath := scanner.HistoryBasePath()
		if basePath == "" {
			continue
		}

		sessions, err := scanner.ExtractSessions(basePath, knownMtimes)
		if err != nil {
			idx.logger.Warn("scanner error", "base", basePath, "error", err)
			continue
		}
		allSessions = append(allSessions, sessions...)
	}

	// Batch-lookup agent names from live_sessions
	sessionIDs := make([]string, len(allSessions))
	for i, s := range allSessions {
		sessionIDs[i] = s.SessionID
	}
	agentNames, _ := idx.store.GetAgentNames(ctx, sessionIDs)

	for _, s := range allSessions {
		si := &store.SessionIndex{
			SessionID:      s.SessionID,
			SourceType:     s.SourceType,
			SourceFile:     s.SourceFile,
			FirstTimestamp: s.FirstTimestamp,
			LastTimestamp:  s.LastTimestamp,
			MessageCount:   s.MessageCount,
			DisplaySummary: s.DisplaySummary,
			FileMtime:      s.FileMtime,
		}
		if names, ok := agentNames[s.SessionID]; ok {
			si.AgentName = names[0]
			si.DisplayName = names[1]
		}
		if err := idx.store.UpsertSessionIndex(ctx, si); err != nil {
			idx.logger.Warn("upsert session index failed", "session", s.SessionID, "error", err)
			continue
		}

		if s.FTSBody != "" {
			if err := idx.store.UpsertFTS(ctx, s.SessionID, s.FTSBody); err != nil {
				idx.logger.Warn("upsert FTS failed", "session", s.SessionID, "error", err)
			}
		}

		indexed++
	}

	if os.Getenv("CORAL_DEBUG_PHASES") == "1" {
		idx.logger.Info("indexer pass complete", "indexed", indexed, "scanners", len(idx.scanners), "sessions", len(allSessions), "duration_ms", time.Since(started).Milliseconds())
	} else {
		idx.logger.Info("indexer pass complete", "indexed", indexed)
	}
	return nil
}

// BatchSummarizer polls summarizer_queue for pending sessions.
type BatchSummarizer struct {
	store       *store.SessionStore
	summarizeFn func(ctx context.Context, sessionID string) error
	logger      *slog.Logger
}

// NewBatchSummarizer creates a new BatchSummarizer.
// The summarizeFn is called for each pending session.
func NewBatchSummarizer(sessionStore *store.SessionStore, summarizeFn func(ctx context.Context, sessionID string) error) *BatchSummarizer {
	return &BatchSummarizer{
		store:       sessionStore,
		summarizeFn: summarizeFn,
		logger:      slog.Default().With("service", "batch_summarizer"),
	}
}

// Run starts the summarization loop. Blocks until context is cancelled.
func (bs *BatchSummarizer) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pending, err := bs.store.GetPendingSummaries(ctx, 5)
		if err != nil {
			bs.logger.Error("get pending summaries failed", "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(30 * time.Second):
			}
			continue
		}

		if len(pending) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(30 * time.Second):
			}
			continue
		}

		for _, sessionID := range pending {
			if err := bs.summarizeFn(ctx, sessionID); err != nil {
				bs.logger.Warn("summarization failed", "session", sessionID, "error", err)
				errMsg := err.Error()
				bs.store.MarkSummarized(ctx, sessionID, "failed", &errMsg)
			} else {
				bs.store.MarkSummarized(ctx, sessionID, "done", nil)
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
}
