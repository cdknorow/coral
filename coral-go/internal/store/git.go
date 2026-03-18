package store

import (
	"context"
	"database/sql"
	"time"

	_ "github.com/jmoiron/sqlx"
)

// GitSnapshot represents a git state snapshot for an agent.
type GitSnapshot struct {
	ID               int64   `db:"id" json:"id"`
	AgentName        string  `db:"agent_name" json:"agent_name"`
	AgentType        string  `db:"agent_type" json:"agent_type"`
	WorkingDirectory string  `db:"working_directory" json:"working_directory"`
	Branch           string  `db:"branch" json:"branch"`
	CommitHash       string  `db:"commit_hash" json:"commit_hash"`
	CommitSubject    string  `db:"commit_subject" json:"commit_subject"`
	CommitTimestamp  *string `db:"commit_timestamp" json:"commit_timestamp"`
	SessionID        *string `db:"session_id" json:"session_id"`
	RemoteURL        *string `db:"remote_url" json:"remote_url"`
	RecordedAt       string  `db:"recorded_at" json:"recorded_at"`
}

// ChangedFile represents a file change in a git working tree.
type ChangedFile struct {
	ID               int64  `db:"id" json:"-"`
	AgentName        string `db:"agent_name" json:"-"`
	SessionID        *string `db:"session_id" json:"-"`
	WorkingDirectory string `db:"working_directory" json:"-"`
	Filepath         string `db:"filepath" json:"filepath"`
	Additions        int    `db:"additions" json:"additions"`
	Deletions        int    `db:"deletions" json:"deletions"`
	Status           string `db:"status" json:"status"`
	RecordedAt       string `db:"recorded_at" json:"recorded_at"`
}

// GitStore provides git snapshot and changed file operations.
type GitStore struct {
	db *DB
}

// NewGitStore creates a new GitStore.
func NewGitStore(db *DB) *GitStore {
	return &GitStore{db: db}
}

// UpsertGitSnapshot inserts or updates a git snapshot.
func (s *GitStore) UpsertGitSnapshot(ctx context.Context, snap *GitSnapshot) error {
	now := time.Now().UTC().Format(time.RFC3339)
	snap.RecordedAt = now

	// Insert or ignore (dedup on session_id + commit_hash)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO git_snapshots
		 (agent_name, agent_type, working_directory, branch, commit_hash,
		  commit_subject, commit_timestamp, session_id, remote_url, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snap.AgentName, snap.AgentType, snap.WorkingDirectory, snap.Branch,
		snap.CommitHash, snap.CommitSubject, snap.CommitTimestamp,
		snap.SessionID, snap.RemoteURL, now)
	if err != nil {
		return err
	}

	// Always update branch/working_directory on the latest row for this session
	updateSQL := "UPDATE git_snapshots SET branch = ?, working_directory = ?, recorded_at = ?"
	args := []interface{}{snap.Branch, snap.WorkingDirectory, now}
	if snap.RemoteURL != nil {
		updateSQL += ", remote_url = ?"
		args = append(args, *snap.RemoteURL)
	}
	updateSQL += " WHERE session_id = ? AND commit_hash = ?"
	args = append(args, snap.SessionID, snap.CommitHash)

	_, err = s.db.ExecContext(ctx, updateSQL, args...)
	return err
}

// GetGitSnapshots returns recent snapshots for an agent.
func (s *GitStore) GetGitSnapshots(ctx context.Context, agentName string, limit int) ([]GitSnapshot, error) {
	var snaps []GitSnapshot
	err := s.db.SelectContext(ctx, &snaps,
		`SELECT id, agent_name, agent_type, working_directory, branch,
		        commit_hash, commit_subject, commit_timestamp,
		        session_id, remote_url, recorded_at
		 FROM git_snapshots WHERE agent_name = ?
		 ORDER BY recorded_at DESC LIMIT ?`,
		agentName, limit)
	return snaps, err
}

// GetLatestGitState returns the most recent snapshot for an agent.
func (s *GitStore) GetLatestGitState(ctx context.Context, agentName string) (*GitSnapshot, error) {
	var snap GitSnapshot
	err := s.db.GetContext(ctx, &snap,
		`SELECT id, agent_name, agent_type, working_directory, branch,
		        commit_hash, commit_subject, commit_timestamp,
		        session_id, remote_url, recorded_at
		 FROM git_snapshots WHERE agent_name = ?
		 ORDER BY recorded_at DESC LIMIT 1`,
		agentName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &snap, err
}

// GetLatestGitStateBySession returns the most recent snapshot for a session.
func (s *GitStore) GetLatestGitStateBySession(ctx context.Context, sessionID string) (*GitSnapshot, error) {
	var snap GitSnapshot
	err := s.db.GetContext(ctx, &snap,
		`SELECT id, agent_name, agent_type, working_directory, branch,
		        commit_hash, commit_subject, commit_timestamp,
		        session_id, remote_url, recorded_at
		 FROM git_snapshots WHERE session_id = ?
		 ORDER BY recorded_at DESC LIMIT 1`,
		sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &snap, err
}

// GetAllLatestGitState returns the latest snapshot per agent/session, keyed by session_id and agent_name.
func (s *GitStore) GetAllLatestGitState(ctx context.Context) (map[string]*GitSnapshot, error) {
	var snaps []GitSnapshot
	err := s.db.SelectContext(ctx, &snaps,
		`SELECT g.id, g.agent_name, g.agent_type, g.working_directory, g.branch,
		        g.commit_hash, g.commit_subject, g.commit_timestamp,
		        g.session_id, g.remote_url, g.recorded_at
		 FROM git_snapshots g
		 INNER JOIN (
		     SELECT COALESCE(session_id, agent_name) AS grp_key,
		            MAX(recorded_at) as max_ts
		     FROM git_snapshots GROUP BY grp_key
		 ) latest ON COALESCE(g.session_id, g.agent_name) = latest.grp_key
		            AND g.recorded_at = latest.max_ts`)
	if err != nil {
		return nil, err
	}

	result := make(map[string]*GitSnapshot, len(snaps))
	for i := range snaps {
		snap := &snaps[i]
		if snap.SessionID != nil {
			result[*snap.SessionID] = snap
		}
		result[snap.AgentName] = snap
	}
	return result, nil
}

// GetGitSnapshotsForSession returns commits linked to a session (with time-range fallback).
func (s *GitStore) GetGitSnapshotsForSession(ctx context.Context, sessionID string, limit int) ([]GitSnapshot, error) {
	var snaps []GitSnapshot
	err := s.db.SelectContext(ctx, &snaps,
		`SELECT id, agent_name, agent_type, working_directory, branch,
		        commit_hash, commit_subject, commit_timestamp,
		        session_id, remote_url, recorded_at
		 FROM git_snapshots WHERE session_id = ?
		 ORDER BY commit_timestamp ASC LIMIT ?`,
		sessionID, limit)
	if err != nil {
		return nil, err
	}
	if len(snaps) > 0 {
		return snaps, nil
	}

	// Fallback: match by time range from session_index
	var idx struct {
		FirstTS *string `db:"first_timestamp"`
		LastTS  *string `db:"last_timestamp"`
	}
	err = s.db.GetContext(ctx, &idx,
		"SELECT first_timestamp, last_timestamp FROM session_index WHERE session_id = ?",
		sessionID)
	if err != nil || idx.FirstTS == nil || idx.LastTS == nil {
		return nil, nil
	}

	err = s.db.SelectContext(ctx, &snaps,
		`SELECT id, agent_name, agent_type, working_directory, branch,
		        commit_hash, commit_subject, commit_timestamp,
		        session_id, remote_url, recorded_at
		 FROM git_snapshots
		 WHERE commit_timestamp >= ? AND commit_timestamp <= ?
		 ORDER BY commit_timestamp ASC LIMIT ?`,
		*idx.FirstTS, *idx.LastTS, limit)
	return snaps, err
}

// ReplaceChangedFiles replaces all changed-file records for an agent/session.
func (s *GitStore) ReplaceChangedFiles(ctx context.Context, agentName, workingDir string, files []ChangedFile, sessionID *string) error {
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete old records
	if sessionID != nil {
		tx.ExecContext(ctx, "DELETE FROM git_changed_files WHERE session_id = ?", *sessionID)
	} else {
		tx.ExecContext(ctx, "DELETE FROM git_changed_files WHERE agent_name = ? AND session_id IS NULL", agentName)
	}

	// Insert fresh records
	for _, f := range files {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO git_changed_files
			 (agent_name, session_id, working_directory, filepath, additions, deletions, status, recorded_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			agentName, sessionID, workingDir, f.Filepath, f.Additions, f.Deletions, f.Status, now)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetChangedFiles returns current changed files for an agent/session.
func (s *GitStore) GetChangedFiles(ctx context.Context, agentName string, sessionID *string) ([]ChangedFile, error) {
	var files []ChangedFile
	if sessionID != nil {
		err := s.db.SelectContext(ctx, &files,
			`SELECT filepath, additions, deletions, status, recorded_at
			 FROM git_changed_files WHERE session_id = ? ORDER BY filepath`,
			*sessionID)
		return files, err
	}
	err := s.db.SelectContext(ctx, &files,
		`SELECT filepath, additions, deletions, status, recorded_at
		 FROM git_changed_files WHERE agent_name = ? AND session_id IS NULL ORDER BY filepath`,
		agentName)
	return files, err
}

// GetAllChangedFileCounts returns file counts per agent/session.
func (s *GitStore) GetAllChangedFileCounts(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		Key   string `db:"key"`
		Count int    `db:"cnt"`
	}
	err := s.db.SelectContext(ctx, &rows,
		`SELECT COALESCE(session_id, agent_name) AS key, COUNT(*) AS cnt
		 FROM git_changed_files GROUP BY key`)
	if err != nil {
		return nil, err
	}
	result := make(map[string]int, len(rows))
	for _, r := range rows {
		result[r.Key] = r.Count
	}
	return result, nil
}
