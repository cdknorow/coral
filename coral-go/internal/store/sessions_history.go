package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/subgentic/coral/internal/ptr"
)

// SessionListParams holds parameters for paginated session listing.
type SessionListParams struct {
	Page           int
	PageSize       int
	Search         string
	FTSMode        string // "phrase", "and", "or"
	TagIDs         []int64
	TagLogic       string // "AND" or "OR"
	SourceTypes    []string
	DateFrom       string
	DateTo         string
	MinDurationSec *int
	MaxDurationSec *int
	AgentName      string
}

// SessionListResult holds paginated session results.
type SessionListResult struct {
	Sessions []SessionListItem `json:"sessions"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// SessionListItem is an enriched session for list display.
type SessionListItem struct {
	SessionID      string  `json:"session_id"`
	SourceType     string  `json:"source_type"`
	SourceFile     string  `json:"source_file"`
	FirstTimestamp *string `json:"first_timestamp"`
	LastTimestamp  *string `json:"last_timestamp"`
	MessageCount   int     `json:"message_count"`
	Summary        string  `json:"summary"`
	// FirstUserMessage is what the person typed first ("" when the row predates it): History titles lead with it (#6207).
	FirstUserMessage string  `json:"first_user_message"`
	SummaryTitle     string  `json:"summary_title"`
	HasNotes         bool    `json:"has_notes"`
	Tags             []Tag   `json:"tags"`
	Branch           *string `json:"branch"`
	DurationSec      *int    `json:"duration_sec"`
	AgentName        string  `json:"agent_name,omitempty"`
	DisplayName      string  `json:"display_name,omitempty"`
}

// ListSessionsPaged returns a paginated, filtered list of sessions.
func (s *SessionStore) ListSessionsPaged(ctx context.Context, params SessionListParams) (*SessionListResult, error) {
	if params.Page < 1 {
		params.Page = 1
	}
	if params.PageSize < 1 {
		params.PageSize = 50
	}
	if params.FTSMode == "" {
		// Natural-language searches should return the best partial matches instead
		// of disappearing as soon as one query word is absent. FTS rank still puts
		// sessions matching more (and rarer) terms first; callers can explicitly
		// request "and" when every word is required.
		params.FTSMode = "or"
	}
	q := sessionListQuery(params)
	total, rows, err := s.pageSessions(ctx, q, params)
	if err != nil {
		return nil, err
	}
	sessionIDs := make([]string, len(rows))
	for i, r := range rows {
		sessionIDs[i] = r.SessionID
	}
	var more sessionListExtras
	if len(sessionIDs) > 0 {
		more = s.sessionListExtras(ctx, sessionIDs)
	}
	sessions := make([]SessionListItem, len(rows))
	for i, r := range rows {
		sessions[i] = more.item(r)
	}
	return &SessionListResult{
		Sessions: sessions,
		Total:    total,
		Page:     params.Page,
		PageSize: params.PageSize,
	}, nil
}

// sessionListSQL is a session list's FROM, WHERE (with its arguments, a slice for an IN) and ORDER BY.
type sessionListSQL struct {
	from, where, order string
	args               []interface{}
}

// sessionListQuery turns the list's filters into SQL: a full-text search (ordered by rank), the dates, the
// duration, the tags (all or any), the source types and the agent name; newest first without a search.
func sessionListQuery(params SessionListParams) sessionListSQL {
	q := sessionListSQL{from: "session_index si", order: "si.last_timestamp DESC"}
	var whereClauses []string
	where := func(clause string, arg interface{}) {
		whereClauses = append(whereClauses, clause)
		q.args = append(q.args, arg)
	}
	if params.Search != "" {
		if safeQ := sanitizeFTSQuery(params.Search, params.FTSMode); safeQ != "" {
			q.from += " JOIN session_fts fts ON fts.session_id = si.session_id"
			where("session_fts MATCH ?", safeQ)
			q.order = "rank"
		}
	}
	if params.DateFrom != "" {
		where("si.last_timestamp >= ?", params.DateFrom+"T00:00:00")
	}
	if params.DateTo != "" {
		// Through the end of the date_to day, fractions of a second included: "~" sorts after every character of a timestamp, so
		// "< date~" is every timestamp that begins with a date up to date_to (a session ending at 23:59:59.5 is not after "T23:59:59").
		where("si.last_timestamp < ?", params.DateTo+"~")
	}
	if params.MinDurationSec != nil {
		where("(julianday(si.last_timestamp) - julianday(si.first_timestamp)) * 86400 >= ?", *params.MinDurationSec)
	}
	if params.MaxDurationSec != nil {
		where("(julianday(si.last_timestamp) - julianday(si.first_timestamp)) * 86400 <= ?", *params.MaxDurationSec)
	}
	if len(params.TagIDs) > 0 && params.TagLogic == "AND" {
		for _, tid := range params.TagIDs {
			where("si.session_id IN (SELECT session_id FROM session_tags WHERE tag_id = ?)", tid)
		}
	} else if len(params.TagIDs) > 0 {
		where("si.session_id IN (SELECT session_id FROM session_tags WHERE tag_id IN (?))", params.TagIDs)
	}
	if len(params.SourceTypes) > 0 {
		where("si.source_type IN (?)", params.SourceTypes)
	}
	if params.AgentName != "" {
		where("si.agent_name = ?", params.AgentName)
	}
	if len(whereClauses) > 0 {
		q.where = " WHERE " + strings.Join(whereClauses, " AND ")
	}
	return q
}

// sessionPageRow is one session_index row of a list page.
type sessionPageRow struct {
	SessionID        string  `db:"session_id"`
	SourceType       string  `db:"source_type"`
	SourceFile       string  `db:"source_file"`
	FirstTimestamp   *string `db:"first_timestamp"`
	LastTimestamp    *string `db:"last_timestamp"`
	MessageCount     int     `db:"message_count"`
	DisplaySummary   string  `db:"display_summary"`
	FirstUserMessage string  `db:"first_user_message"`
	AgentName        *string `db:"agent_name"`
	DisplayName      *string `db:"display_name"`
}

// pageSessions counts the sessions the query matches and reads the requested page of them.
func (s *SessionStore) pageSessions(ctx context.Context, q sessionListSQL, params SessionListParams) (int, []sessionPageRow, error) {
	countSQL, countArgs, err := sqlx.In(fmt.Sprintf("SELECT COUNT(*) FROM %s%s", q.from, q.where), q.args...)
	if err != nil {
		return 0, nil, err
	}
	var total int
	if err := s.reader().GetContext(ctx, &total, countSQL, countArgs...); err != nil {
		return 0, nil, err
	}
	offset := (params.Page - 1) * params.PageSize
	selectFields := `si.session_id, si.source_type, si.source_file,
		si.first_timestamp, si.last_timestamp, si.message_count, si.display_summary, si.first_user_message,
		si.agent_name, si.display_name`
	query, pageArgs, err := sqlx.In(fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT ? OFFSET ?", selectFields, q.from, q.where, q.order),
		append(q.args, params.PageSize, offset)...)
	if err != nil {
		return 0, nil, err
	}
	var rows []sessionPageRow
	if err := s.reader().SelectContext(ctx, &rows, query, pageArgs...); err != nil {
		return 0, nil, err
	}
	return total, rows, nil
}

// sessionListExtras is what a list page shows beyond session_index, per session id: notes, tags, the latest git
// branch, and the live session's agent and display names. A lookup that fails leaves its part empty.
type sessionListExtras struct {
	meta   map[string]sessionNotesMeta
	tags   map[string][]Tag
	branch map[string]string
	agent  map[string]liveAgentNames
}

type sessionNotesMeta struct {
	HasNotes     bool
	IsUserEdited bool
	SummaryTitle string
}

type liveAgentNames struct {
	AgentName   string
	DisplayName string
}

func (s *SessionStore) sessionListExtras(ctx context.Context, sessionIDs []string) sessionListExtras {
	return sessionListExtras{
		meta:   s.sessionNotesMeta(ctx, sessionIDs),
		tags:   s.sessionTagsOf(ctx, sessionIDs),
		branch: s.sessionLatestBranches(ctx, sessionIDs),
		agent:  s.liveAgentNamesOf(ctx, sessionIDs),
	}
}

// sessionNotesMeta is whether each session has notes (its own or an auto summary) and its title: the first header
// of its notes, else of its auto summary.
func (s *SessionStore) sessionNotesMeta(ctx context.Context, sessionIDs []string) map[string]sessionNotesMeta {
	out := map[string]sessionNotesMeta{}
	metaQuery, metaArgs, _ := sqlx.In(
		"SELECT session_id, notes_md, auto_summary, is_user_edited FROM session_meta WHERE session_id IN (?)",
		sessionIDs)
	var metaRows []struct {
		SessionID    string `db:"session_id"`
		NotesMD      string `db:"notes_md"`
		AutoSummary  string `db:"auto_summary"`
		IsUserEdited bool   `db:"is_user_edited"`
	}
	if err := s.reader().SelectContext(ctx, &metaRows, metaQuery, metaArgs...); err == nil {
		for _, r := range metaRows {
			content := r.NotesMD
			if content == "" {
				content = r.AutoSummary
			}
			out[r.SessionID] = sessionNotesMeta{
				HasNotes:     r.NotesMD != "" || r.AutoSummary != "",
				IsUserEdited: r.IsUserEdited,
				SummaryTitle: extractFirstHeader(content),
			}
		}
	}
	return out
}

// sessionTagsOf is each session's tags, by name.
func (s *SessionStore) sessionTagsOf(ctx context.Context, sessionIDs []string) map[string][]Tag {
	out := map[string][]Tag{}
	tagQuery, tagArgs, _ := sqlx.In(
		`SELECT st.session_id, t.id, t.name, t.color
		 FROM session_tags st JOIN tags t ON t.id = st.tag_id
		 WHERE st.session_id IN (?) ORDER BY t.name`,
		sessionIDs)
	var tagRows []struct {
		SessionID string `db:"session_id"`
		ID        int64  `db:"id"`
		Name      string `db:"name"`
		Color     string `db:"color"`
	}
	if err := s.reader().SelectContext(ctx, &tagRows, tagQuery, tagArgs...); err == nil {
		for _, r := range tagRows {
			out[r.SessionID] = append(out[r.SessionID], Tag{ID: r.ID, Name: r.Name, Color: r.Color})
		}
	}
	return out
}

// sessionLatestBranches is each session's git branch at its latest snapshot.
func (s *SessionStore) sessionLatestBranches(ctx context.Context, sessionIDs []string) map[string]string {
	out := map[string]string{}
	branchQuery, branchArgs, _ := sqlx.In(
		`SELECT gs.session_id, gs.branch
		 FROM git_snapshots gs
		 INNER JOIN (
		     SELECT session_id, MAX(recorded_at) as max_ts
		     FROM git_snapshots WHERE session_id IN (?)
		     GROUP BY session_id
		 ) latest ON gs.session_id = latest.session_id AND gs.recorded_at = latest.max_ts`,
		sessionIDs)
	var branchRows []struct {
		SessionID string `db:"session_id"`
		Branch    string `db:"branch"`
	}
	if err := s.reader().SelectContext(ctx, &branchRows, branchQuery, branchArgs...); err == nil {
		for _, r := range branchRows {
			out[r.SessionID] = r.Branch
		}
	}
	return out
}

// liveAgentNamesOf is each live session's agent and display names.
func (s *SessionStore) liveAgentNamesOf(ctx context.Context, sessionIDs []string) map[string]liveAgentNames {
	out := map[string]liveAgentNames{}
	agentQuery, agentArgs, _ := sqlx.In(
		`SELECT session_id, agent_name, display_name FROM live_sessions WHERE session_id IN (?)`,
		sessionIDs)
	var agentRows []struct {
		SessionID   string  `db:"session_id"`
		AgentName   string  `db:"agent_name"`
		DisplayName *string `db:"display_name"`
	}
	if err := s.reader().SelectContext(ctx, &agentRows, agentQuery, agentArgs...); err == nil {
		for _, r := range agentRows {
			names := liveAgentNames{AgentName: r.AgentName}
			if r.DisplayName != nil {
				names.DisplayName = *r.DisplayName
			}
			out[r.SessionID] = names
		}
	}
	return out
}

// item is one list row: session_index's fields, its notes, tags and branch, and its names (session_index's, with
// a live session's non-empty ones over them: more current for active agents).
func (x sessionListExtras) item(r sessionPageRow) SessionListItem {
	tags := x.tags[r.SessionID]
	if tags == nil {
		tags = []Tag{}
	}
	var branch *string
	if b, ok := x.branch[r.SessionID]; ok {
		branch = &b
	}
	agentName, displayName := ptr.Value(r.AgentName), ptr.Value(r.DisplayName)
	if info, ok := x.agent[r.SessionID]; ok {
		if info.AgentName != "" {
			agentName = info.AgentName
		}
		if info.DisplayName != "" {
			displayName = info.DisplayName
		}
	}
	meta := x.meta[r.SessionID]
	return SessionListItem{
		SessionID:        r.SessionID,
		SourceType:       r.SourceType,
		SourceFile:       r.SourceFile,
		FirstTimestamp:   r.FirstTimestamp,
		LastTimestamp:    r.LastTimestamp,
		MessageCount:     r.MessageCount,
		Summary:          r.DisplaySummary,
		FirstUserMessage: r.FirstUserMessage,
		SummaryTitle:     meta.SummaryTitle,
		HasNotes:         meta.HasNotes,
		Tags:             tags,
		Branch:           branch,
		DurationSec:      computeDuration(r.FirstTimestamp, r.LastTimestamp),
		AgentName:        agentName,
		DisplayName:      displayName,
	}
}

// GetSessionDuration returns the duration of a session in seconds.
// For active sessions, duration is calculated from created_at to now.
// For stopped sessions, duration is calculated from created_at to stopped_at.
func (s *SessionStore) GetSessionDuration(ctx context.Context, sessionID string) (float64, error) {
	var result struct {
		CreatedAt string  `db:"created_at"`
		StoppedAt *string `db:"stopped_at"`
		Status    string  `db:"status"`
	}
	err := s.reader().GetContext(ctx, &result,
		"SELECT created_at, stopped_at, status FROM live_sessions WHERE session_id = ?", sessionID)
	if err != nil {
		return 0, err
	}

	start, err := time.Parse(time.RFC3339Nano, result.CreatedAt)
	if err != nil {
		start, err = time.Parse("2006-01-02T15:04:05Z", result.CreatedAt)
		if err != nil {
			return 0, fmt.Errorf("parse created_at: %w", err)
		}
	}

	var end time.Time
	if result.StoppedAt != nil && *result.StoppedAt != "" {
		end, err = time.Parse(time.RFC3339Nano, *result.StoppedAt)
		if err != nil {
			end, err = time.Parse("2006-01-02T15:04:05Z", *result.StoppedAt)
			if err != nil {
				return 0, fmt.Errorf("parse stopped_at: %w", err)
			}
		}
	} else {
		end = time.Now().UTC()
	}

	return end.Sub(start).Seconds(), nil
}

// GetRecentStoppedSessions returns recently stopped sessions for timeline display.
func (s *SessionStore) GetRecentStoppedSessions(ctx context.Context, since string, limit int) ([]LiveSession, error) {
	var sessions []LiveSession
	query := `SELECT session_id, agent_type, agent_name, working_dir, display_name,
		 board_name, icon, created_at, stopped_at
		 FROM live_sessions WHERE status = 'inactive' AND stopped_at IS NOT NULL`
	args := []interface{}{}
	if since != "" {
		query += " AND stopped_at >= ?"
		args = append(args, since)
	}
	query += " ORDER BY stopped_at DESC, rowid DESC LIMIT ?"
	args = append(args, limit)
	err := s.reader().SelectContext(ctx, &sessions, query, args...)
	return sessions, err
}

// SessionLineageItem represents an authenticated session or ancestor in a resume chain.
type SessionLineageItem struct {
	SessionID    string  `db:"session_id" json:"session_id"`
	AgentType    string  `db:"agent_type" json:"agent_type"`
	AgentName    string  `db:"agent_name" json:"agent_name"`
	WorkingDir   string  `db:"working_dir" json:"working_dir"`
	DisplayName  *string `db:"display_name" json:"display_name,omitempty"`
	ResumeFromID *string `db:"resume_from_id" json:"resume_from_id,omitempty"`
	SourceFile   string  `db:"source_file" json:"source_file,omitempty"`
	IsCurrent    bool    `db:"-" json:"is_current"`
}

// GetSessionAncestry returns the caller session and all proven ancestor sessions
// reached via resume_from_id links in live_sessions and session_index.
func (s *SessionStore) GetSessionAncestry(ctx context.Context, sessionID string) ([]SessionLineageItem, error) {
	if sessionID == "" {
		return nil, nil
	}

	ls, err := s.GetLiveSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if ls == nil {
		// Not in live_sessions; check if it exists in session_index
		idx, err := s.GetIndexedSession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if idx == nil {
			return nil, nil
		}
		var dispName *string
		if idx.DisplayName != "" {
			dispName = &idx.DisplayName
		}
		return []SessionLineageItem{{
			SessionID:   idx.SessionID,
			AgentType:   idx.SourceType,
			AgentName:   idx.AgentName,
			DisplayName: dispName,
			SourceFile:  idx.SourceFile,
			IsCurrent:   true,
		}}, nil
	}

	rootItem := SessionLineageItem{
		SessionID:    ls.SessionID,
		AgentType:    ls.AgentType,
		AgentName:    ls.AgentName,
		WorkingDir:   ls.WorkingDir,
		DisplayName:  ls.DisplayName,
		ResumeFromID: ls.ResumeFromID,
		IsCurrent:    true,
	}
	if idx, _ := s.GetIndexedSession(ctx, ls.SessionID); idx != nil {
		rootItem.SourceFile = idx.SourceFile
	}
	lineage := []SessionLineageItem{rootItem}

	visited := map[string]bool{ls.SessionID: true}
	currResume := ls.ResumeFromID

	for i := 0; i < 50 && currResume != nil && *currResume != ""; i++ {
		ancestorID := strings.TrimSpace(*currResume)
		if ancestorID == "" || visited[ancestorID] {
			break
		}
		visited[ancestorID] = true

		// 1. Try live_sessions first
		ancLS, err := s.GetLiveSession(ctx, ancestorID)
		if err == nil && ancLS != nil {
			item := SessionLineageItem{
				SessionID:    ancLS.SessionID,
				AgentType:    ancLS.AgentType,
				AgentName:    ancLS.AgentName,
				WorkingDir:   ancLS.WorkingDir,
				DisplayName:  ancLS.DisplayName,
				ResumeFromID: ancLS.ResumeFromID,
				IsCurrent:    false,
			}
			if idx, _ := s.GetIndexedSession(ctx, ancLS.SessionID); idx != nil {
				item.SourceFile = idx.SourceFile
			}
			lineage = append(lineage, item)
			currResume = ancLS.ResumeFromID
			continue
		}

		// 2. Try session_index by exact session_id
		ancIdx, err := s.GetIndexedSession(ctx, ancestorID)
		if err == nil && ancIdx != nil {
			var dispName *string
			if ancIdx.DisplayName != "" {
				dispName = &ancIdx.DisplayName
			}
			item := SessionLineageItem{
				SessionID:   ancIdx.SessionID,
				AgentType:   ancIdx.SourceType,
				AgentName:   ancIdx.AgentName,
				DisplayName: dispName,
				SourceFile:  ancIdx.SourceFile,
				IsCurrent:   false,
			}
			lineage = append(lineage, item)
			break
		}

		// 3. Try session_index by native identifier in source_file
		var matchedIdx SessionIndex
		err = s.db.GetContext(ctx, &matchedIdx,
			`SELECT session_id, source_type, source_file, first_timestamp, last_timestamp,
			        message_count, display_summary, agent_name, display_name, indexed_at, file_mtime
			 FROM session_index WHERE source_file LIKE ? LIMIT 1`, "%"+ancestorID+"%")
		if err == nil && matchedIdx.SessionID != "" {
			var dispName *string
			if matchedIdx.DisplayName != "" {
				dispName = &matchedIdx.DisplayName
			}
			item := SessionLineageItem{
				SessionID:   matchedIdx.SessionID,
				AgentType:   matchedIdx.SourceType,
				AgentName:   matchedIdx.AgentName,
				DisplayName: dispName,
				SourceFile:  matchedIdx.SourceFile,
				IsCurrent:   false,
			}
			lineage = append(lineage, item)
			break
		}

		break
	}

	return lineage, nil
}
