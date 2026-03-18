package background

import (
	"context"
	"log/slog"
	"time"

	"github.com/cdknorow/coral/internal/store"
)

// IndexedSession holds extracted session data from a history file.
type IndexedSession struct {
	SessionID      string
	SourceType     string
	FirstTimestamp *string
	LastTimestamp   *string
	MessageCount   int
	DisplaySummary string
	FTSBody        string
}

// HistoryScanner is the interface that agent implementations must satisfy
// to participate in session indexing.
type HistoryScanner interface {
	// HistoryBasePath returns the root directory for this agent's history files.
	HistoryBasePath() string
	// HistoryGlobPattern returns the glob pattern for history files (e.g., "*.jsonl").
	HistoryGlobPattern() string
	// ExtractSessions parses a history file and returns indexed session data.
	ExtractSessions(filePath string) ([]IndexedSession, error)
}

// SessionIndexer scans history files for all registered agents and upserts
// into session_index + session_fts.
type SessionIndexer struct {
	store         *store.SessionStore
	scanners      []HistoryScanner
	interval      time.Duration
	startupDelay  time.Duration
	logger        *slog.Logger
}

// NewSessionIndexer creates a new SessionIndexer.
func NewSessionIndexer(sessionStore *store.SessionStore, scanners []HistoryScanner, interval, startupDelay time.Duration) *SessionIndexer {
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
	// Wait for startup delay
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(idx.startupDelay):
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
	knownMtimes, err := idx.store.GetIndexedMtimes(ctx)
	if err != nil {
		return err
	}

	var indexed, skipped int

	for _, scanner := range idx.scanners {
		basePath := scanner.HistoryBasePath()
		if basePath == "" {
			continue
		}

		// Use filepath.Glob to find history files
		// In a full implementation, this would use scanner.HistoryGlobPattern()
		// with recursive directory walking. For now, the scanners handle
		// their own file discovery internally.
		sessions, err := scanner.ExtractSessions(basePath)
		if err != nil {
			idx.logger.Warn("scanner error", "base", basePath, "error", err)
			continue
		}

		for _, s := range sessions {
			// Check mtime cache (simplified — full impl checks per-file)
			_ = knownMtimes // Used in full implementation

			err := idx.store.UpsertSessionIndex(ctx, &store.SessionIndex{
				SessionID:      s.SessionID,
				SourceType:     s.SourceType,
				SourceFile:     basePath,
				FirstTimestamp: s.FirstTimestamp,
				LastTimestamp:   s.LastTimestamp,
				MessageCount:   s.MessageCount,
				DisplaySummary: s.DisplaySummary,
				FileMtime:      float64(time.Now().Unix()),
			})
			if err != nil {
				idx.logger.Warn("upsert session index failed", "session", s.SessionID, "error", err)
				continue
			}

			if s.FTSBody != "" {
				if err := idx.store.UpsertFTS(ctx, s.SessionID, s.FTSBody); err != nil {
					idx.logger.Warn("upsert FTS failed", "session", s.SessionID, "error", err)
				}
			}

			if err := idx.store.EnqueueForSummarization(ctx, s.SessionID); err != nil {
				idx.logger.Warn("enqueue summarization failed", "session", s.SessionID, "error", err)
			}

			indexed++
		}
	}

	idx.logger.Info("indexer pass complete", "indexed", indexed, "skipped", skipped)
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
