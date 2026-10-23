package background

import (
	"context"
	"errors"
	"github.com/subgentic/coral/internal/logsafe"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentsignin"
	"github.com/subgentic/coral/internal/store"
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

// indexerFTSBatch is how many sessions' search text one pass collects before writing it (store.UpsertFTSBatch).
const indexerFTSBatch = 100

// RunOnce performs a single indexing pass over all registered scanners.
func (idx *SessionIndexer) RunOnce(ctx context.Context) error {
	started := time.Now()
	knownMtimes, err := idx.store.GetHistoryMtimes(ctx, agent.HistoryIndexVersion)
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
			idx.logger.Warn("scanner error", "base", logsafe.Dir(basePath), "error", err)
			continue
		}
		allSessions = append(allSessions, sessions...)
	}

	// Batch-lookup agent names from live_sessions
	sessionIDs := make([]string, 0, len(allSessions))
	for _, s := range allSessions {
		if !s.Excluded {
			sessionIDs = append(sessionIDs, s.SessionID)
		}
	}
	agentNames, _ := idx.store.GetAgentNames(ctx, sessionIDs)

	// The search text is written in batches: a delete of a session's old text scans the whole search table, so one delete for a batch
	// of sessions is one scan instead of one for each (#4669). If a batch fails its sessions are written one by one, to find the one.
	var pending []store.FTSItem
	flush := func() {
		if len(pending) == 0 {
			return
		}
		if err := idx.store.UpsertFTSBatch(ctx, pending); err != nil {
			idx.logger.Warn("upsert FTS batch failed, writing its sessions one by one", "sessions", len(pending), "error", err)
			for _, it := range pending {
				if err := idx.store.UpsertFTS(ctx, it.SessionID, it.Body); err != nil {
					idx.logger.Warn("upsert FTS failed", "session", it.SessionID, "error", err)
				}
			}
		}
		pending = pending[:0]
	}
	for _, s := range allSessions {
		if s.Excluded {
			if err := idx.store.RemoveExcludedSessionSource(ctx, s.SourceType, s.SourceFile, s.FileMtime, s.IndexVersion); err != nil {
				return err
			}
			continue
		}
		si := &store.SessionIndex{
			SessionID:        s.SessionID,
			SourceType:       s.SourceType,
			SourceFile:       s.SourceFile,
			FirstTimestamp:   s.FirstTimestamp,
			LastTimestamp:    s.LastTimestamp,
			MessageCount:     s.MessageCount,
			DisplaySummary:   s.DisplaySummary,
			FirstUserMessage: s.FirstUserMessage,
			FileMtime:        s.FileMtime,
			IndexVersion:     s.IndexVersion,
		}
		if names, ok := agentNames[s.SessionID]; ok {
			si.AgentName = names[0]
			si.DisplayName = names[1]
		}
		if err := idx.store.UpsertSessionIndex(ctx, si); err != nil {
			idx.logger.Warn("upsert session index failed", "session", s.SessionID, "error", err)
			continue
		}

		if s.FTSBody != "" && !s.FTSUnchanged {
			pending = append(pending, store.FTSItem{SessionID: s.SessionID, Body: s.FTSBody})
			if len(pending) >= indexerFTSBatch {
				flush()
			}
		}

		indexed++
	}
	flush()

	// Exclusion marks of rollout files that are gone are dropped (#6098); only a file known not to exist counts as gone.
	if n, err := idx.store.PruneHistoryExclusions(ctx, rolloutStillThere); err != nil {
		idx.logger.Warn("pruning history exclusions failed", "error", err)
	} else if n > 0 {
		idx.logger.Info("forgot history exclusions of rollout files that no longer exist", "count", n)
	}

	if os.Getenv("CORAL_DEBUG_PHASES") == "1" {
		idx.logger.Info("indexer pass complete", "indexed", indexed, "scanners", len(idx.scanners), "sessions", len(allSessions), "duration_ms", time.Since(started).Milliseconds())
	} else {
		idx.logger.Info("indexer pass complete", "indexed", indexed, "duration_ms", time.Since(started).Milliseconds())
	}
	return nil
}

// rolloutStillThere reports whether a history file exists: false only when it is known to be gone, so a file that cannot be checked keeps its exclusion row.
func rolloutStillThere(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// BatchSummarizer polls summarizer_queue for pending sessions.
type BatchSummarizer struct {
	store       *store.SessionStore
	summarizeFn func(ctx context.Context, sessionID string) error
	logger      *slog.Logger
}

// NewBatchSummarizer creates a new BatchSummarizer.
// signedOutWait is how long the batch summarizer leaves its pending sessions alone when Claude is not signed in.
var signedOutWait = 5 * time.Minute

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
				if agentsignin.IsNotSignedIn(err) { // Claude is signed out (#5469): nothing is marked failed, and the batch waits before it asks again
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(signedOutWait):
					}
					break
				}
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
