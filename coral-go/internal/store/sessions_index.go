package store

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

// ── Session Index ──────────────────────────────────────────────────────

// UpsertSessionIndex inserts or replaces a session index entry.
func (s *SessionStore) UpsertSessionIndex(ctx context.Context, idx *SessionIndex) error {
	now := nowUTC()
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// A Codex rollout file represents exactly one session. Remove stale aliases
		// created if an older parser extracted a malformed Coral session marker.
		if idx.SourceType == "codex" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM history_exclusions WHERE source_type = ? AND source_file = ?`, idx.SourceType, idx.SourceFile); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM session_fts WHERE session_id IN
				 (SELECT session_id FROM session_index WHERE source_type = 'codex'
				  AND source_file = ? AND session_id <> ?)`,
				idx.SourceFile, idx.SessionID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM session_index WHERE source_type = 'codex'
				 AND source_file = ? AND session_id <> ?`,
				idx.SourceFile, idx.SessionID); err != nil {
				return err
			}
		}

		_, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO session_index
			 (session_id, source_type, source_file, first_timestamp, last_timestamp,
			  message_count, display_summary, agent_name, display_name, indexed_at, file_mtime, index_version)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			idx.SessionID, idx.SourceType, idx.SourceFile,
			idx.FirstTimestamp, idx.LastTimestamp,
			idx.MessageCount, idx.DisplaySummary,
			idx.AgentName, idx.DisplayName, now, idx.FileMtime, idx.IndexVersion)
		return err
	})
}

// GetIndexedSession returns the persisted source metadata for a history ID.
// History IDs may be Coral marker IDs rather than provider-native IDs, so
// callers must use the source file to recover provider-specific identity.
func (s *SessionStore) GetIndexedSession(ctx context.Context, sessionID string) (*SessionIndex, error) {
	var idx SessionIndex
	err := s.reader().GetContext(ctx, &idx,
		`SELECT session_id, source_type, source_file, first_timestamp, last_timestamp,
			message_count, display_summary, agent_name, display_name, indexed_at, file_mtime, index_version
		 FROM session_index WHERE session_id = ?`, sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &idx, nil
}

// EnqueueForSummarization adds a session to the summarization queue.
// If the session already exists with a 'failed' status, it is reset to 'pending'
// so it will be retried (e.g. after re-indexing with a corrected source_file).
func (s *SessionStore) EnqueueForSummarization(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO summarizer_queue (session_id, status) VALUES (?, 'pending')
		 ON CONFLICT(session_id) DO UPDATE SET status = 'pending', error_msg = NULL
		 WHERE status = 'failed'`,
		sessionID)
	return err
}

// MarkSummarized updates the summarization status for a session.
func (s *SessionStore) MarkSummarized(ctx context.Context, sessionID, status string, errMsg *string) error {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		"UPDATE summarizer_queue SET status = ?, attempted_at = ?, error_msg = ? WHERE session_id = ?",
		status, now, errMsg, sessionID)
	return err
}

// GetPendingSummaries returns session IDs pending summarization.
func (s *SessionStore) GetPendingSummaries(ctx context.Context, limit int) ([]string, error) {
	var ids []string
	err := s.reader().SelectContext(ctx, &ids,
		"SELECT session_id FROM summarizer_queue WHERE status = 'pending' LIMIT ?", limit)
	return ids, err
}

// GetAgentNames returns agent_name and display_name for sessions from live_sessions.
func (s *SessionStore) GetAgentNames(ctx context.Context, sessionIDs []string) (map[string][2]string, error) {
	if len(sessionIDs) == 0 {
		return map[string][2]string{}, nil
	}
	query, args, err := sqlx.In(
		"SELECT session_id, agent_name, display_name FROM live_sessions WHERE session_id IN (?)",
		sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SessionID   string  `db:"session_id"`
		AgentName   string  `db:"agent_name"`
		DisplayName *string `db:"display_name"`
	}
	if err := s.reader().SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	result := make(map[string][2]string, len(rows))
	for _, r := range rows {
		dn := ""
		if r.DisplayName != nil {
			dn = *r.DisplayName
		}
		result[r.SessionID] = [2]string{r.AgentName, dn}
	}
	return result, nil
}

// indexedMtimesSQL lists the indexed files that also have a session_fts row. The set of session ids in session_fts is read once
// (an uncorrelated IN): session_fts is an FTS5 table, so a lookup by session_id is a scan of its whole content, and a correlated
// EXISTS made one scan, of every transcript's text, for each indexed session on every indexer pass (4.1 s for 250 sessions of
// 1 MB; it grew with the square of the history, #4669).
const indexedMtimesSQL = `SELECT si.source_file, si.file_mtime FROM session_index si
	 WHERE si.session_id IN (SELECT session_id FROM session_fts)`

// GetIndexedMtimes returns source_file -> file_mtime for sessions that are
// fully indexed, so the indexer can skip re-reading files that have not changed.
//
// A session is only "fully indexed" if it also has a full-text row. Sessions
// indexed before FTSBody was populated have a session_index row and no
// session_fts row; without this join they would be skipped forever on mtime
// and stay permanently unsearchable. Excluding them here makes the next
// indexer pass re-read exactly those files and backfill them, once, with no
// migration and no flag.
func (s *SessionStore) GetIndexedMtimes(ctx context.Context) (map[string]float64, error) {
	var rows []struct {
		SourceFile string  `db:"source_file"`
		FileMtime  float64 `db:"file_mtime"`
	}
	err := s.reader().SelectContext(ctx, &rows, indexedMtimesSQL)
	if err != nil {
		return nil, err
	}
	result := make(map[string]float64, len(rows))
	for _, r := range rows {
		if existing, ok := result[r.SourceFile]; !ok || r.FileMtime > existing {
			result[r.SourceFile] = r.FileMtime
		}
	}
	return result, nil
}

// RemoveExcludedSessionSource forgets derived History/search data for a rollout
// identified by its scanner as an internal thread. Source files and user data stay.
func (s *SessionStore) RemoveExcludedSessionSource(ctx context.Context, sourceType, sourceFile string, mtime float64, version int) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for _, query := range []string{
			`DELETE FROM session_fts WHERE session_id IN (SELECT session_id FROM session_index WHERE source_type = ? AND source_file = ?)`,
			`DELETE FROM summarizer_queue WHERE session_id IN (SELECT session_id FROM session_index WHERE source_type = ? AND source_file = ?)`,
			`DELETE FROM session_index WHERE source_type = ? AND source_file = ?`,
		} {
			if _, err := tx.ExecContext(ctx, query, sourceType, sourceFile); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO history_exclusions (source_type, source_file, file_mtime, index_version) VALUES (?, ?, ?, ?)`, sourceType, sourceFile, mtime, version)
		return err
	})
}

// GetHistoryMtimes skips files checked by the current parser. Legacy Codex rows
// are parsed once even with unchanged mtimes; excluded files keep only this
// derived stat cache, so a later pass needn't reopen guardian rollouts either.
// Version 1 is the guardian-aware Codex parser (#6081).
func (s *SessionStore) GetHistoryMtimes(ctx context.Context) (map[string]float64, error) {
	known, err := s.GetIndexedMtimes(ctx)
	if err != nil {
		return nil, err
	}
	var legacy []string
	if err := s.reader().SelectContext(ctx, &legacy, `SELECT source_file FROM session_index WHERE source_type = 'codex' AND index_version < 1`); err != nil {
		return nil, err
	}
	for _, path := range legacy {
		delete(known, path)
	}
	var excluded []struct {
		SourceFile string  `db:"source_file"`
		FileMtime  float64 `db:"file_mtime"`
	}
	if err := s.reader().SelectContext(ctx, &excluded, `SELECT source_file, file_mtime FROM history_exclusions WHERE source_type = 'codex' AND index_version = 1`); err != nil {
		return nil, err
	}
	for _, row := range excluded {
		known[row.SourceFile] = row.FileMtime
	}
	return known, nil
}
