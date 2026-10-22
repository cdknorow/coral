package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// ── Live Sessions ─────────────────────────────────────────────────────

// RegisterLiveSession registers a new live session.
// SetCLIVersion records the version of the CLI a session's agent runs (#3481).
func (s *SessionStore) SetCLIVersion(ctx context.Context, sessionID, version string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET cli_version = NULLIF(?, '') WHERE session_id = ?", version, sessionID)
	return err
}

// SetHooksRev records the hook set a session's agent runs (#3665): at launch, from the set Coral
// installed, and from the revision its own hook events carry. It only ever goes up: an older or
// missing value never replaces a newer one (an old Coral hook left in the user's global
// settings would otherwise flip it).
func (s *SessionStore) SetHooksRev(ctx context.Context, sessionID string, rev int) error {
	if rev <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET hooks_rev = ? WHERE session_id = ? AND (hooks_rev IS NULL OR hooks_rev < ?)", rev, sessionID, rev)
	return err
}

// SetHookDir records the folder an agent was started with its Coral tools on its PATH (#6089): at every launch, restart, resume and wake. The agent's hooks are programs found there, so a
// folder that is gone later (the app replaced or moved while the agent ran) means its hooks can no longer run.
func (s *SessionStore) SetHookDir(ctx context.Context, sessionID, dir string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET hook_dir = ? WHERE session_id = ?", dir, sessionID)
	return err
}

// RunTagOf is the tag recorded for a session ("" if none): see SetRunTagIfEmpty.
func (s *SessionStore) RunTagOf(ctx context.Context, sessionID string) string {
	var tag sql.NullString
	if err := s.db.Reader().GetContext(ctx, &tag, "SELECT run_tag FROM live_sessions WHERE session_id = ?", sessionID); err != nil {
		return ""
	}
	return tag.String
}

// SetRunTagIfEmpty records the tag a tmux agent's processes carry (CORAL_PTY_RUN, #6141) unless the session has one already: the tag stays the same for the life of the row. A session with no row is
// left alone.
func (s *SessionStore) SetRunTagIfEmpty(ctx context.Context, sessionID, tag string) error {
	if tag == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET run_tag = ? WHERE session_id = ? AND (run_tag IS NULL OR run_tag = '')", tag, sessionID)
	return err
}

// HookDirs returns the recorded tools folder of every live session that has one.
func (s *SessionStore) HookDirs(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		ID  string `db:"session_id"`
		Dir string `db:"hook_dir"`
	}
	if err := s.db.Reader().SelectContext(ctx, &rows, "SELECT session_id, hook_dir FROM live_sessions WHERE hook_dir IS NOT NULL AND hook_dir != ''"); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Dir
	}
	return out, nil
}

// HooksRevs returns the recorded hook set of every live session that has one.
func (s *SessionStore) HooksRevs(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		ID  string `db:"session_id"`
		Rev int    `db:"hooks_rev"`
	}
	if err := s.db.Reader().SelectContext(ctx, &rows, "SELECT session_id, hooks_rev FROM live_sessions WHERE hooks_rev IS NOT NULL"); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Rev
	}
	return out, nil
}

// CLIVersion returns the recorded CLI version of one session, or "".
func (s *SessionStore) CLIVersion(ctx context.Context, sessionID string) string {
	var v sql.NullString
	_ = s.db.Reader().GetContext(ctx, &v, "SELECT cli_version FROM live_sessions WHERE session_id = ?", sessionID)
	return v.String
}

// CLIVersions returns the recorded CLI version of each live session that has one.
func (s *SessionStore) CLIVersions(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		SessionID string `db:"session_id"`
		Version   string `db:"cli_version"`
	}
	if err := s.db.Reader().SelectContext(ctx, &rows, "SELECT session_id, cli_version FROM live_sessions WHERE cli_version IS NOT NULL"); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.SessionID] = r.Version
	}
	return out, nil
}

func (s *SessionStore) RegisterLiveSession(ctx context.Context, ls *LiveSession) error {
	if ls.CreatedAt == "" {
		ls.CreatedAt = nowUTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO live_sessions
		 (session_id, agent_type, agent_name, working_dir, display_name, resume_from_id, flags, is_job, prompt, board_name, board_server, backend, icon, is_sleeping, board_type, capabilities, model, thinking, context_window, tools, pid, status, created_at, permission_mode)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		ls.SessionID, ls.AgentType, ls.AgentName, ls.WorkingDir,
		ls.DisplayName, ls.ResumeFromID, ls.Flags, ls.IsJob,
		ls.Prompt, ls.BoardName, ls.BoardServer, ls.Backend, ls.Icon, ls.IsSleeping, ls.BoardType,
		ls.Capabilities, ls.Model, ls.Thinking, ls.ContextWindow, ls.Tools, ls.PID, ls.CreatedAt, ls.PermissionMode)
	return err
}

// UnregisterLiveSession marks a live session as inactive (preserves the record for history).
func (s *SessionStore) UnregisterLiveSession(ctx context.Context, sessionID string) error {
	defer s.db.InvalidateSessionTokens() // after the commit (or the rollback): never before it (#4303)
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := clearAutoAcceptTx(ctx, tx, sessionID, AutoAcceptRunEnded, false); err != nil {
			return err
		}
		// The session is over: so is its agent token (#3387).
		if err := revokeSessionTokensTx(ctx, tx, sessionID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE live_sessions SET status = 'inactive', stopped_at = ? WHERE session_id = ?",
			nowUTC(), sessionID)
		return err
	})
}

// GetStoppedSessions returns, for the given session IDs, the ones whose
// live_sessions row is marked inactive, mapped to their stopped_at timestamp
// (empty string if none recorded). Used to recognise orphaned runtime
// sessions: tmux/PTY sessions that are still running even though Coral has
// already stopped them.
func (s *SessionStore) GetStoppedSessions(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	result := map[string]string{}
	if len(sessionIDs) == 0 {
		return result, nil
	}
	query, args, err := sqlx.In(
		"SELECT session_id, COALESCE(stopped_at, '') AS stopped_at FROM live_sessions WHERE status = 'inactive' AND session_id IN (?)",
		sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SessionID string `db:"session_id"`
		StoppedAt string `db:"stopped_at"`
	}
	if err := s.reader().SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	for _, r := range rows {
		result[r.SessionID] = r.StoppedAt
	}
	return result, nil
}

// GetAllLiveSessions returns all registered live sessions.
func (s *SessionStore) GetAllLiveSessions(ctx context.Context) ([]LiveSession, error) {
	var sessions []LiveSession
	err := s.reader().SelectContext(ctx, &sessions,
		`SELECT session_id, agent_type, agent_name, working_dir, display_name,
		 resume_from_id, flags, is_job, prompt, board_name, board_server, backend, icon, name_color, is_sleeping, is_archived, sleep_reason, board_type, capabilities, model, thinking, observed_model, context_window, tools, pid, worktree_path, worktree_repo, team_id, created_at, stopped_at, permission_mode
		 FROM live_sessions WHERE status = 'active' ORDER BY created_at, rowid`)
	return sessions, err
}

// CountActiveLiveSessions returns the number of non-sleeping live sessions.
func (s *SessionStore) CountActiveLiveSessions(ctx context.Context) (int, error) {
	var count int
	err := s.reader().GetContext(ctx, &count,
		"SELECT COUNT(*) FROM live_sessions WHERE is_sleeping = 0 AND status = 'active'")
	return count, err
}

// GetBoardSessions returns all live sessions on a given board.
func (s *SessionStore) GetBoardSessions(ctx context.Context, boardName string) ([]LiveSession, error) {
	var sessions []LiveSession
	err := s.reader().SelectContext(ctx, &sessions,
		`SELECT session_id, agent_type, agent_name, working_dir, display_name,
		 resume_from_id, flags, is_job, prompt, board_name, board_server, backend, icon, is_sleeping, is_archived, board_type, capabilities, model, thinking, tools, pid, worktree_path, worktree_repo, team_id, created_at, stopped_at, permission_mode
		 FROM live_sessions WHERE board_name = ? AND status = 'active' ORDER BY created_at, rowid`, boardName)
	return sessions, err
}

// CountLiveSessions returns the total number of live sessions (including sleeping).
func (s *SessionStore) CountLiveSessions(ctx context.Context) (int, error) {
	var count int
	err := s.reader().GetContext(ctx, &count,
		"SELECT COUNT(*) FROM live_sessions WHERE status = 'active'")
	return count, err
}

// HasRunAgents reports whether this install has ever started an agent (a row in live_sessions, active or not): what tells an install that has been used from a new one.
func (s *SessionStore) HasRunAgents(ctx context.Context) (bool, error) {
	var any bool
	err := s.reader().GetContext(ctx, &any, "SELECT EXISTS(SELECT 1 FROM live_sessions)")
	return any, err
}

// UpdateSessionPID stores the shell process PID for a live session.
func (s *SessionStore) UpdateSessionPID(ctx context.Context, sessionID string, pid int) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET pid = ? WHERE session_id = ?", pid, sessionID)
	return err
}

// SetTeamID sets the team_id on a live session.
func (s *SessionStore) SetTeamID(ctx context.Context, sessionID string, teamID int64) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET team_id = ? WHERE session_id = ?", teamID, sessionID)
	return err
}

// LiveSessionsByPIDs returns every active session whose recorded process is one of pids.
// A recorded pid is only where the session's process WAS: once that process ends the
// system can reuse the number for anything, so a caller must check the session's live
// process before trusting a match (#3864).
func (s *SessionStore) LiveSessionsByPIDs(ctx context.Context, pids []int) ([]LiveSession, error) {
	if len(pids) == 0 {
		return nil, fmt.Errorf("no PIDs provided")
	}
	query, args, err := sqlx.In("SELECT session_id, agent_type, agent_name, working_dir, display_name, board_name, backend, pid FROM live_sessions WHERE pid IN (?) AND pid > 0 AND status = 'active'", pids)
	if err != nil {
		return nil, err
	}
	var out []LiveSession
	err = s.reader().SelectContext(ctx, &out, s.db.Rebind(query), args...)
	return out, err
}

// CountBoardSessions returns the number of live sessions on a given board.
func (s *SessionStore) CountBoardSessions(ctx context.Context, boardName string) (int, error) {
	var count int
	err := s.reader().GetContext(ctx, &count,
		"SELECT COUNT(*) FROM live_sessions WHERE board_name = ? AND status = 'active'", boardName)
	return count, err
}

// GetLiveSessionPromptInfo returns prompt, board_name, and board_server for a live session.
func (s *SessionStore) GetLiveSessionPromptInfo(ctx context.Context, sessionID string) (*LiveSession, error) {
	var ls LiveSession
	err := s.reader().GetContext(ctx, &ls,
		"SELECT prompt, board_name, board_server, board_type FROM live_sessions WHERE session_id = ?", sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ls, err
}

// LiveSessionState is what the terminal-log cleanup needs to know about a session: whether it has ended.
type LiveSessionState struct {
	AgentType string `db:"agent_type"`
	Status    string `db:"status"`
	Sleeping  bool   `db:"is_sleeping"`
	Archived  bool   `db:"is_archived"`
}

// LiveSessionStateByID is one session row's state, ok false when there is no such row: asked again for each terminal log just before it is removed (#4447).
func (s *SessionStore) LiveSessionStateByID(ctx context.Context, id string) (LiveSessionState, bool, error) {
	var rows []LiveSessionState
	if err := s.reader().SelectContext(ctx, &rows, "SELECT agent_type, status, is_sleeping, is_archived FROM live_sessions WHERE session_id = ?", id); err != nil {
		return LiveSessionState{}, false, err
	}
	if len(rows) != 1 {
		return LiveSessionState{}, false, nil
	}
	return rows[0], true, nil
}

// LiveSessionStates returns every session row's state by session id, whatever its status: the cleanup of an ended agent's terminal log (#4447) asks
// this and keeps anything it cannot match to a row that is stopped, awake-less (not sleeping) and not archived.
func (s *SessionStore) LiveSessionStates(ctx context.Context) (map[string]LiveSessionState, error) {
	var rows []struct {
		SessionID string `db:"session_id"`
		LiveSessionState
	}
	if err := s.reader().SelectContext(ctx, &rows, "SELECT session_id, agent_type, status, is_sleeping, is_archived FROM live_sessions"); err != nil {
		return nil, err
	}
	out := make(map[string]LiveSessionState, len(rows))
	for _, r := range rows {
		out[r.SessionID] = r.LiveSessionState
	}
	return out, nil
}

// GetLiveSession returns a full live session record by session_id.
func (s *SessionStore) GetLiveSession(ctx context.Context, sessionID string) (*LiveSession, error) {
	var ls LiveSession
	err := s.reader().GetContext(ctx, &ls,
		`SELECT session_id, agent_type, agent_name, working_dir, display_name,
		 resume_from_id, flags, is_job, prompt, board_name, board_server, backend, icon, name_color, is_sleeping, is_archived, sleep_reason, board_type, git_diff_mode, capabilities, model, thinking, observed_model, context_window, tools, pid, worktree_path, worktree_repo, team_id, created_at, stopped_at, status, permission_mode
		 FROM live_sessions WHERE session_id = ?`, sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ls, err
}

// ReplaceLiveSession replaces an old session with a new one, carrying forward metadata.
func (s *SessionStore) ReplaceLiveSession(ctx context.Context, oldSessionID string, newSession *LiveSession) error {
	defer s.db.InvalidateSessionTokens() // after the commit (or the rollback): never before it (#4303)
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// A restart or resume replaces the session: the old one's token stops working (#3387).
		if err := revokeSessionTokensTx(ctx, tx, oldSessionID); err != nil {
			return err
		}
		// Carry forward flags, prompt, board from old session if not set
		var old LiveSession
		err := tx.GetContext(ctx, &old,
			"SELECT agent_type, flags, prompt, board_name, board_server, icon, name_color, is_sleeping, board_type, display_name, is_job, backend, capabilities, model, thinking, context_window, tools, permission_mode FROM live_sessions WHERE session_id = ?",
			oldSessionID)
		if err == nil {
			sameProvider := newSession.AgentType == "" || newSession.AgentType == old.AgentType
			if newSession.Flags == nil && sameProvider {
				newSession.Flags = old.Flags
			}
			if newSession.Prompt == nil {
				newSession.Prompt = old.Prompt
			}
			if newSession.BoardName == nil {
				newSession.BoardName = old.BoardName
			}
			if newSession.BoardServer == nil {
				newSession.BoardServer = old.BoardServer
			}
			if newSession.Icon == nil {
				newSession.Icon = old.Icon
			}
			if newSession.NameColor == nil {
				newSession.NameColor = old.NameColor
			}
			if newSession.BoardType == nil {
				newSession.BoardType = old.BoardType
			}
			if newSession.DisplayName == nil {
				newSession.DisplayName = old.DisplayName
			}
			if newSession.Capabilities == nil && sameProvider {
				newSession.Capabilities = old.Capabilities
			}
			if newSession.Model == nil && sameProvider {
				newSession.Model = old.Model
			}
			if newSession.Thinking == nil && sameProvider {
				newSession.Thinking = old.Thinking // the level belongs to the provider: another one's set differs
			}
			if newSession.Model == nil && newSession.ContextWindow == 0 && sameProvider {
				newSession.ContextWindow = old.ContextWindow
			}
			if newSession.Tools == nil {
				newSession.Tools = old.Tools
			}
			if newSession.Backend == nil {
				newSession.Backend = old.Backend
			}
			if newSession.PermissionMode == nil {
				newSession.PermissionMode = old.PermissionMode
			}
			newSession.IsSleeping = old.IsSleeping
			if newSession.IsJob == 0 {
				newSession.IsJob = old.IsJob
			}
		}

		// The agent keeps its place in the list (#4943): in the saved order, and by the time that place was taken.
		if err := carrySessionOrderTx(ctx, tx, oldSessionID, newSession.SessionID); err != nil {
			return err
		}
		var listAt sql.NullString
		if err := tx.GetContext(ctx, &listAt, "SELECT COALESCE(list_at, created_at) FROM live_sessions WHERE session_id = ?", oldSessionID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		// The new row starts without auto-accept (a new agent run).
		if err := clearAutoAcceptTx(ctx, tx, oldSessionID, AutoAcceptAgentRestarted, false); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE live_sessions SET status = 'inactive', stopped_at = ? WHERE session_id = ?", nowUTC(), oldSessionID); err != nil {
			return err
		}
		// A team member follows its agent to the new session (#4074): kill, resurrect and the
		// knowledge distiller find the member by its current session id.
		if _, err := tx.ExecContext(ctx, "UPDATE team_members SET session_id = ? WHERE session_id = ?", newSession.SessionID, oldSessionID); err != nil {
			return err
		}

		now := nowUTC()
		_, err = tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO live_sessions
			 (session_id, agent_type, agent_name, working_dir, display_name, resume_from_id, flags, is_job, prompt, board_name, board_server, backend, icon, name_color, is_sleeping, board_type, capabilities, model, thinking, context_window, tools, pid, status, created_at, list_at, permission_mode)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)`,
			newSession.SessionID, newSession.AgentType, newSession.AgentName,
			newSession.WorkingDir, newSession.DisplayName, newSession.ResumeFromID,
			newSession.Flags, newSession.IsJob, newSession.Prompt, newSession.BoardName,
			newSession.BoardServer, newSession.Backend, newSession.Icon, newSession.NameColor, newSession.IsSleeping, newSession.BoardType,
			newSession.Capabilities, newSession.Model, newSession.Thinking, newSession.ContextWindow, newSession.Tools, newSession.PID, now, listAt, newSession.PermissionMode)
		return err
	})
}

// CountSessionsWithWorktree returns the number of live sessions using a given worktree path.
func (s *SessionStore) CountSessionsWithWorktree(ctx context.Context, worktreePath string) (int, error) {
	var count int
	err := s.db.GetContext(ctx, &count,
		"SELECT COUNT(*) FROM live_sessions WHERE worktree_path = ? AND status = 'active'", worktreePath)
	return count, err
}

// UpdateWorktreeInfo sets the worktree path and source repo on a live session.
func (s *SessionStore) UpdateWorktreeInfo(ctx context.Context, sessionID, worktreePath, repoPath string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET worktree_path = ?, worktree_repo = ? WHERE session_id = ?",
		worktreePath, repoPath, sessionID)
	return err
}

// SetIcon sets or clears the emoji icon for a live session.
func (s *SessionStore) SetIcon(ctx context.Context, sessionID string, icon *string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET icon = ? WHERE session_id = ?",
		icon, sessionID)
	return err
}

// UpdateContextWindow updates the context window and optionally the model for a live session.
func (s *SessionStore) UpdateContextWindow(ctx context.Context, sessionID string, contextWindow int, model string) error {
	if model != "" {
		_, err := s.db.ExecContext(ctx,
			"UPDATE live_sessions SET context_window = ?, model = ? WHERE session_id = ?",
			contextWindow, model, sessionID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET context_window = ? WHERE session_id = ?",
		contextWindow, sessionID)
	return err
}

// SetObservedModel records the model a hook reported for a live session (display only).
func (s *SessionStore) SetObservedModel(ctx context.Context, sessionID, model string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET observed_model = ? WHERE session_id = ?", model, sessionID)
	return err
}

// SetNameColor sets or clears the sidebar name colour for a live session.
func (s *SessionStore) SetNameColor(ctx context.Context, sessionID string, color *string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET name_color = ? WHERE session_id = ?",
		color, sessionID)
	return err
}

// GetNameColors returns {session_id: name_color} for sessions that have one set.
func (s *SessionStore) GetNameColors(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}
	query, args, err := sqlx.In(
		"SELECT session_id, name_color FROM live_sessions WHERE session_id IN (?) AND name_color IS NOT NULL AND name_color != ''",
		sessionIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.reader().QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var sid, color string
		if err := rows.Scan(&sid, &color); err == nil {
			result[sid] = color
		}
	}
	return result, nil
}

// GetIcons returns {session_id: icon} for sessions that have an icon set.
func (s *SessionStore) GetIcons(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}
	query, args, err := sqlx.In(
		"SELECT session_id, icon FROM live_sessions WHERE session_id IN (?) AND icon IS NOT NULL AND icon != ''",
		sessionIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.reader().QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var sid, icon string
		if err := rows.Scan(&sid, &icon); err == nil {
			result[sid] = icon
		}
	}
	if err := rows.Err(); err != nil { // an error mid-iteration is an error, not a short map (#5318)
		return nil, err
	}
	return result, nil
}

// ── Sleep/Wake ────────────────────────────────────────────────────────

// SetBoardSleeping sets is_sleeping for all sessions on a board. Returns the count of affected rows.
func (s *SessionStore) SetBoardSleeping(ctx context.Context, boardName string, sleeping bool) (int, error) {
	val := 0
	if sleeping {
		val = 1
	}
	query := "UPDATE live_sessions SET is_sleeping = ? WHERE board_name = ?"
	if !sleeping {
		query = "UPDATE live_sessions SET is_sleeping = ?, is_archived = 0 WHERE board_name = ?"
	}
	result, err := s.db.ExecContext(ctx, query, val, boardName)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// SetSessionSleeping sets is_sleeping for a single session. Waking also clears
// is_archived: an archived session is always a sleeping one.
func (s *SessionStore) SetSessionSleeping(ctx context.Context, sessionID string, sleeping bool) error {
	return s.setSessionSleeping(ctx, sessionID, sleeping, "")
}

// SleepReasonTerminalGone: Coral put the agent to sleep because its terminal session no
// longer exists (killed, or its tmux server died), not because the user asked (#3344).
const SleepReasonTerminalGone = "terminal_gone"

// SleepReasonAgentExited: Coral put the agent to sleep because its CLI exited and left its pane at a plain shell
// (#6058). The dashboard says "Ended" and offers Restart, not a plain "Sleeping". Waking clears it.
const SleepReasonAgentExited = "agent_exited"

// SleepReasonCoralStopped: the agent was running when a pty Coral stopped (cleanly, or a crash
// found at the next start), so Coral may wake it again at its next start (#3636). Only Coral's
// own shutdown and startup code writes it; no request can (an Operator's Sleep is reason "").
const SleepReasonCoralStopped = "coral_stopped"

// SleepReasonHooksUpdate: Coral is resuming the agent on the current hook set (#3665): Sleep
// then Wake. Only that route writes it; a crash between the two leaves it for the next start's
// auto-wake, as an agent Coral stopped.
const SleepReasonHooksUpdate = "hooks_update"

// SetSessionSleepingFor puts an agent to sleep for a reason Coral found (#3344), shown in the
// dashboard instead of a plain "Sleeping". Waking clears it.
func (s *SessionStore) SetSessionSleepingFor(ctx context.Context, sessionID, reason string) error {
	return s.setSessionSleeping(ctx, sessionID, true, reason)
}

func (s *SessionStore) setSessionSleeping(ctx context.Context, sessionID string, sleeping bool, reason string) error {
	query := "UPDATE live_sessions SET is_sleeping = 1, sleep_reason = NULLIF(?, '') WHERE session_id = ?"
	args := []any{reason, sessionID}
	code := AutoAcceptAgentSlept
	if !sleeping {
		query = "UPDATE live_sessions SET is_sleeping = 0, is_archived = 0, sleep_reason = NULL WHERE session_id = ?"
		args = []any{sessionID}
		code = AutoAcceptAgentWoken
	}
	// Sleeping stops the agent process and waking starts a new one: either way
	// auto-accept is cleared, so it is never on for a run that did not enable it.
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := clearAutoAcceptTx(ctx, tx, sessionID, code, false); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

// SetSessionArchived archives a session (archived implies sleeping) or
// unarchives it, which leaves it sleeping. It does not stop a running agent;
// the caller does that first.
func (s *SessionStore) SetSessionArchived(ctx context.Context, sessionID string, archived bool) error {
	if !archived {
		_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET is_archived = 0 WHERE session_id = ?", sessionID)
		return err
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := clearAutoAcceptTx(ctx, tx, sessionID, AutoAcceptAgentArchived, false); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE live_sessions SET is_sleeping = 1, is_archived = 1 WHERE session_id = ?", sessionID)
		return err
	})
}

// IsSessionArchived reports whether an active live session is archived.
func (s *SessionStore) IsSessionArchived(ctx context.Context, sessionID string) (bool, error) {
	var archived bool
	err := s.reader().GetContext(ctx, &archived,
		"SELECT EXISTS(SELECT 1 FROM live_sessions WHERE session_id = ? AND status = 'active' AND is_archived = 1)", sessionID)
	return archived, err
}

// GetSleepingBoardNames returns board names where ALL sessions are sleeping.
// A board with any awake session is not considered sleeping.
func (s *SessionStore) GetSleepingBoardNames(ctx context.Context) ([]string, error) {
	var names []string
	err := s.reader().SelectContext(ctx, &names,
		`SELECT board_name FROM live_sessions WHERE board_name IS NOT NULL AND status = 'active'
		 GROUP BY board_name HAVING COUNT(*) = SUM(CASE WHEN is_sleeping = 1 THEN 1 ELSE 0 END)`)
	if err != nil {
		return nil, err
	}
	return names, nil
}

// CleanupOrphanedSleeping removes sleeping session rows where an awake
// version with the same display_name and board_name already exists.
// This cleans up duplicates from old wake code that created new sessions.
// Archived rows are kept: the operator put that copy away on purpose.
func (s *SessionStore) CleanupOrphanedSleeping(ctx context.Context) (int, error) {
	now := nowUTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE live_sessions SET status = 'inactive', stopped_at = ? WHERE is_sleeping = 1 AND status = 'active'
		AND session_id IN (
			SELECT sleeping.session_id FROM live_sessions sleeping
			INNER JOIN live_sessions awake
			ON sleeping.board_name = awake.board_name
			AND sleeping.display_name = awake.display_name
			AND sleeping.is_sleeping = 1
			AND sleeping.is_archived = 0
			AND awake.is_sleeping = 0
			AND sleeping.session_id != awake.session_id
			AND sleeping.status = 'active'
			AND awake.status = 'active'
		)`, now)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// SetSessionThinking records the thinking level a live session now runs with: "" is the CLI's own default (#4969).
func (s *SessionStore) SetSessionThinking(ctx context.Context, sessionID, level string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET thinking = ? WHERE session_id = ?", level, sessionID)
	return err
}
