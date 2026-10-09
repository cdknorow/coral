package store

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

// gitSnapshotCols is the column list for git_snapshots queries.
const gitSnapshotCols = `id, agent_name, agent_type, working_directory, branch,
		        commit_hash, commit_subject, commit_timestamp,
		        session_id, remote_url, recorded_at, COALESCE(pr_number, 0) as pr_number,
		        COALESCE(is_worktree, 0) as is_worktree, COALESCE(base_branch, '') as base_branch,
		        COALESCE(ahead, 0) as ahead, COALESCE(behind, 0) as behind, COALESCE(dirty_count, 0) as dirty_count`

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
	PRNumber         int     `db:"pr_number" json:"pr_number,omitempty"`
	// Worktree is true when the working directory is a linked git worktree
	// rather than the repository's main checkout. BaseBranch is the default
	// branch (main/master) that Ahead/Behind are measured against; DirtyCount
	// is the number of uncommitted paths.
	Worktree   bool   `db:"is_worktree" json:"is_worktree,omitempty"`
	BaseBranch string `db:"base_branch" json:"base_branch,omitempty"`
	Ahead      int    `db:"ahead" json:"ahead,omitempty"`
	Behind     int    `db:"behind" json:"behind,omitempty"`
	DirtyCount int    `db:"dirty_count" json:"dirty_count,omitempty"`
}

// FileAgent records which agent last edited a file and when.
type FileAgent struct {
	Name         string `json:"name"`
	LastEditedAt string `json:"last_edited_at"`
}

// ChangedFile represents a file change in a git working tree.
type ChangedFile struct {
	ID               int64       `db:"id" json:"-"`
	AgentName        string      `db:"agent_name" json:"-"`
	SessionID        *string     `db:"session_id" json:"-"`
	WorkingDirectory string      `db:"working_directory" json:"-"`
	Filepath         string      `db:"filepath" json:"filepath"`
	Additions        int         `db:"additions" json:"additions"`
	Deletions        int         `db:"deletions" json:"deletions"`
	Status           string      `db:"status" json:"status"`
	RecordedAt       string      `db:"recorded_at" json:"recorded_at"`
	Agents           []FileAgent `db:"-" json:"agents,omitempty"`
	Source           string      `db:"-" json:"source,omitempty"`
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
	now := nowUTC()
	snap.RecordedAt = now

	// Insert or ignore (dedup on session_id + commit_hash)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO git_snapshots
		 (agent_name, agent_type, working_directory, branch, commit_hash,
		  commit_subject, commit_timestamp, session_id, remote_url, recorded_at, pr_number,
		  is_worktree, base_branch, ahead, behind, dirty_count)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snap.AgentName, snap.AgentType, snap.WorkingDirectory, snap.Branch,
		snap.CommitHash, snap.CommitSubject, snap.CommitTimestamp,
		snap.SessionID, snap.RemoteURL, now, snap.PRNumber,
		snap.Worktree, snap.BaseBranch, snap.Ahead, snap.Behind, snap.DirtyCount)
	if err != nil {
		return err
	}

	// Always update branch/working_directory on the latest row for this session
	updateSQL := "UPDATE git_snapshots SET branch = ?, working_directory = ?, recorded_at = ?, is_worktree = ?, base_branch = ?, ahead = ?, behind = ?, dirty_count = ?"
	args := []interface{}{snap.Branch, snap.WorkingDirectory, now, snap.Worktree, snap.BaseBranch, snap.Ahead, snap.Behind, snap.DirtyCount}
	if snap.RemoteURL != nil {
		updateSQL += ", remote_url = ?"
		args = append(args, *snap.RemoteURL)
	}
	if snap.PRNumber > 0 {
		updateSQL += ", pr_number = ?"
		args = append(args, snap.PRNumber)
	}
	updateSQL += " WHERE session_id = ? AND commit_hash = ?"
	args = append(args, snap.SessionID, snap.CommitHash)

	_, err = s.db.ExecContext(ctx, updateSQL, args...)
	return err
}

// GetRecentGitSnapshots returns recent snapshots across all agents, optionally filtered by time.
func (s *GitStore) GetRecentGitSnapshots(ctx context.Context, since string, limit int) ([]GitSnapshot, error) {
	var snaps []GitSnapshot
	var err error
	if since != "" {
		err = s.db.SelectContext(ctx, &snaps,
			`SELECT `+gitSnapshotCols+`
			 FROM git_snapshots WHERE recorded_at >= ?
			 ORDER BY recorded_at DESC LIMIT ?`,
			since, limit)
	} else {
		err = s.db.SelectContext(ctx, &snaps,
			`SELECT `+gitSnapshotCols+`
			 FROM git_snapshots ORDER BY recorded_at DESC LIMIT ?`,
			limit)
	}
	return snaps, err
}

// GetGitSnapshots returns recent snapshots for an agent.
func (s *GitStore) GetGitSnapshots(ctx context.Context, agentName string, limit int) ([]GitSnapshot, error) {
	var snaps []GitSnapshot
	err := s.db.SelectContext(ctx, &snaps,
		`SELECT ` + gitSnapshotCols + `
		 FROM git_snapshots WHERE agent_name = ?
		 ORDER BY recorded_at DESC LIMIT ?`,
		agentName, limit)
	return snaps, err
}

// GetLatestGitState returns the most recent snapshot for an agent.
func (s *GitStore) GetLatestGitState(ctx context.Context, agentName string) (*GitSnapshot, error) {
	var snap GitSnapshot
	err := s.db.GetContext(ctx, &snap,
		`SELECT ` + gitSnapshotCols + `
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
		`SELECT ` + gitSnapshotCols + `
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
		        g.session_id, g.remote_url, g.recorded_at, COALESCE(g.pr_number, 0) as pr_number,
		        COALESCE(g.is_worktree, 0) as is_worktree, COALESCE(g.base_branch, '') as base_branch,
		        COALESCE(g.ahead, 0) as ahead, COALESCE(g.behind, 0) as behind, COALESCE(g.dirty_count, 0) as dirty_count
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
		`SELECT ` + gitSnapshotCols + `
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
		`SELECT ` + gitSnapshotCols + `
		 FROM git_snapshots
		 WHERE commit_timestamp >= ? AND commit_timestamp <= ?
		 ORDER BY commit_timestamp ASC LIMIT ?`,
		*idx.FirstTS, *idx.LastTS, limit)
	return snaps, err
}

// ReplaceChangedFiles replaces changed-file records for an agent/session and diff mode.
// Each (session, diff_mode) pair is cached independently so switching modes is instant.
func (s *GitStore) ReplaceChangedFiles(ctx context.Context, agentName, workingDir string, files []ChangedFile, sessionID *string, diffMode string) error {
	now := nowUTC()
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if sessionID != nil {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM git_changed_files WHERE session_id = ? AND diff_mode = ?",
				*sessionID, diffMode); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM git_changed_files WHERE agent_name = ? AND session_id IS NULL AND diff_mode = ?",
				agentName, diffMode); err != nil {
				return err
			}
		}

		for _, f := range files {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO git_changed_files
				 (agent_name, session_id, working_directory, filepath, additions, deletions, status, recorded_at, diff_mode)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				agentName, sessionID, workingDir, f.Filepath, f.Additions, f.Deletions, f.Status, now, diffMode); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetChangedFiles returns cached changed files for an agent/session and diff mode.
// The found return value is false when no cache exists for the requested mode
// (distinct from a cache hit with zero changed files).
func (s *GitStore) GetChangedFiles(ctx context.Context, agentName string, sessionID *string, diffMode string) (files []ChangedFile, found bool, err error) {
	var exists bool
	if sessionID != nil {
		_ = s.db.GetContext(ctx, &exists,
			`SELECT EXISTS(SELECT 1 FROM git_changed_files WHERE session_id = ? AND diff_mode = ?)`,
			*sessionID, diffMode)
	} else {
		_ = s.db.GetContext(ctx, &exists,
			`SELECT EXISTS(SELECT 1 FROM git_changed_files WHERE agent_name = ? AND session_id IS NULL AND diff_mode = ?)`,
			agentName, diffMode)
	}
	if !exists {
		return nil, false, nil
	}

	if sessionID != nil {
		err = s.db.SelectContext(ctx, &files,
			`SELECT filepath, additions, deletions, status, recorded_at
			 FROM git_changed_files WHERE session_id = ? AND diff_mode = ? ORDER BY filepath`,
			*sessionID, diffMode)
	} else {
		err = s.db.SelectContext(ctx, &files,
			`SELECT filepath, additions, deletions, status, recorded_at
			 FROM git_changed_files WHERE agent_name = ? AND session_id IS NULL AND diff_mode = ? ORDER BY filepath`,
			agentName, diffMode)
	}
	return files, true, err
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
