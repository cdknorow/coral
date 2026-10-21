package routes

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/store"
)

// HistoryHandler handles session history / search endpoints.
type HistoryHandler struct {
	ss          *store.SessionStore
	ts          *store.TaskStore
	gs          *store.GitStore
	bs          *board.Store
	cfg         *config.Config
	summarizeFn func(ctx context.Context, sessionID string) error
}

// SetSummarizeFn sets the function used for synchronous summarization.
func (h *HistoryHandler) SetSummarizeFn(fn func(ctx context.Context, sessionID string) error) {
	h.summarizeFn = fn
}

func NewHistoryHandler(db *store.DB, cfg *config.Config, bs *board.Store) *HistoryHandler {
	return &HistoryHandler{
		ss:  store.NewSessionStore(db),
		ts:  store.NewTaskStore(db),
		gs:  store.NewGitStore(db),
		bs:  bs,
		cfg: cfg,
	}
}

// ListSessions returns paginated, filtered history sessions.
// GET /api/sessions/history
func (h *HistoryHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params, chatType := parseHistoryListQuery(q)
	page, pageSize := params.Page, params.PageSize

	// ── Agent sessions ──
	agentSessions := make([]map[string]any, 0)
	agentTotal := 0
	if chatType == "all" || chatType == "agent" {
		result, err := h.ss.ListSessionsPaged(r.Context(), params)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}

		// Cold-start fast path: when DB is empty and no filters active,
		// match Python's file-scan fallback response shape (page_size=0).
		if result.Total == 0 && !historyFiltered(params) && chatType != "group" {
			writeJSON(w, http.StatusOK, map[string]any{
				"sessions":  agentSessions,
				"total":     0,
				"page":      1,
				"page_size": 0,
			})
			return
		}

		agentTotal = result.Total
		agentSessions = append(agentSessions, agentHistoryRows(result.Sessions)...)
	}

	// ── Group chats (board projects) ──
	var boardSessions []map[string]any
	if (chatType == "all" || chatType == "group") && h.bs != nil {
		boardSessions = h.boardHistoryRows(r, q.Get("q"), q.Get("date_from"), q.Get("date_to"))
	}

	// ── Merge and return ──
	var rows []map[string]any
	total := 0
	switch chatType {
	case "agent":
		writeJSON(w, http.StatusOK, map[string]any{
			"sessions":  agentSessions,
			"total":     agentTotal,
			"page":      page,
			"page_size": pageSize,
		})
		return
	case "group":
		rows, total = boardSessions, len(boardSessions)
	default:
		// type == "all": merge agent + board, sorted by last_timestamp desc
		rows, total = mergeByLastTimestamp(agentSessions, boardSessions), agentTotal+len(boardSessions)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":  pageRows(rows, page, pageSize),
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// parseHistoryListQuery reads the history list's query: the page (from 1) and its size (1 to 200, else 50), the
// filters, and the chat type (agent, group, or all). A tag id that isn't a number is skipped; agy and Gemini share
// their history, so either source type brings the other.
func parseHistoryListQuery(q url.Values) (store.SessionListParams, string) {
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("page_size"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}

	var tagIDs []int64
	if raw := q.Get("tag_ids"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				tagIDs = append(tagIDs, id)
			}
		}
	}

	var sourceTypes []string
	if raw := q.Get("source_types"); raw != "" {
		seen := make(map[string]bool)
		for _, s := range strings.Split(raw, ",") {
			st := strings.TrimSpace(s)
			if st == "" || seen[st] {
				continue
			}
			seen[st] = true
			sourceTypes = append(sourceTypes, st)
			if (st == "agy" || st == "antigravity") && !seen["gemini"] {
				seen["gemini"] = true
				sourceTypes = append(sourceTypes, "gemini")
			} else if st == "gemini" && !seen["agy"] {
				seen["agy"] = true
				sourceTypes = append(sourceTypes, "agy")
			}
		}
	}

	params := store.SessionListParams{
		Page:           page,
		PageSize:       pageSize,
		Search:         q.Get("q"),
		FTSMode:        q.Get("fts_mode"),
		TagIDs:         tagIDs,
		TagLogic:       q.Get("tag_logic"),
		SourceTypes:    sourceTypes,
		DateFrom:       q.Get("date_from"),
		DateTo:         q.Get("date_to"),
		MinDurationSec: optionalInt(q.Get("min_duration_sec")),
		MaxDurationSec: optionalInt(q.Get("max_duration_sec")),
		AgentName:      q.Get("agent_name"),
	}

	// Chat type filter: all, agent, group
	chatType := q.Get("type")
	if chatType != "agent" && chatType != "group" {
		chatType = "all"
	}
	return params, chatType
}

// optionalInt is v as a number, or nil when it is empty or not one.
func optionalInt(v string) *int {
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

// historyFiltered reports whether any filter is active (used for the cold-start fast path).
func historyFiltered(params store.SessionListParams) bool {
	return params.Search != "" || len(params.TagIDs) > 0 ||
		len(params.SourceTypes) > 0 || params.DateFrom != "" || params.DateTo != "" ||
		params.MinDurationSec != nil || params.MaxDurationSec != nil ||
		params.AgentName != ""
}

// agentHistoryRows are the agent sessions' rows.
func agentHistoryRows(sessions []store.SessionListItem) []map[string]any {
	rows := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		rows = append(rows, map[string]any{
			"session_id":      s.SessionID,
			"source_type":     s.SourceType,
			"first_timestamp": ptr.Value(s.FirstTimestamp),
			"last_timestamp":  ptr.Value(s.LastTimestamp),
			"message_count":   s.MessageCount,
			"summary":         s.Summary,
			"summary_title":   s.SummaryTitle,
			"has_notes":       s.HasNotes,
			"tags":            s.Tags,
			"branch":          s.Branch,
			"duration_sec":    s.DurationSec,
			"agent_name":      s.AgentName,
			"display_name":    s.DisplayName,
			"type":            "agent",
		})
	}
	return rows
}

// boardHistoryRows are the group chats (board projects): those whose messages or name match the search, with a
// message on or after dateFrom, and one on or before the end of dateTo. None when the boards can't be read.
func (h *HistoryHandler) boardHistoryRows(r *http.Request, searchQ, dateFrom, dateTo string) []map[string]any {
	projects, err := h.bs.ListProjectsEnriched(r.Context())
	if err != nil {
		return nil
	}
	// Filter by text search
	if searchQ != "" {
		matchingProjects, _ := h.bs.SearchMessages(r.Context(), searchQ)
		matchSet := make(map[string]bool, len(matchingProjects))
		for _, p := range matchingProjects {
			matchSet[p] = true
		}
		qLower := strings.ToLower(searchQ)
		filtered := projects[:0]
		for _, p := range projects {
			if matchSet[p.Project] || strings.Contains(strings.ToLower(p.Project), qLower) {
				filtered = append(filtered, p)
			}
		}
		projects = filtered
	}

	// Filter by date
	if dateFrom != "" {
		filtered := projects[:0]
		for _, p := range projects {
			if p.LastMessageAt != nil && *p.LastMessageAt >= dateFrom {
				filtered = append(filtered, p)
			}
		}
		projects = filtered
	}
	if dateTo != "" {
		// Every timestamp that begins with the date_to date is inside the day, whatever follows the seconds (a message at
		// 23:59:59.5Z is not after "T23:59:59"): "~" sorts after every character of a timestamp, so this is the date's prefix.
		cutoff := dateTo + "~"
		filtered := projects[:0]
		for _, p := range projects {
			if p.FirstMessageAt != nil && *p.FirstMessageAt < cutoff {
				filtered = append(filtered, p)
			}
		}
		projects = filtered
	}

	var rows []map[string]any
	for _, p := range projects {
		participants := ""
		if p.ParticipantNames != nil {
			participants = *p.ParticipantNames
		}
		rows = append(rows, map[string]any{
			"session_id":        fmt.Sprintf("board:%s", p.Project),
			"title":             p.Project,
			"type":              "group",
			"source_type":       "board",
			"summary":           fmt.Sprintf("%d messages, %d participants", p.MessageCount, p.SubscriberCount),
			"first_timestamp":   ptr.Value(p.FirstMessageAt),
			"last_timestamp":    ptr.Value(p.LastMessageAt),
			"message_count":     p.MessageCount,
			"subscriber_count":  p.SubscriberCount,
			"participant_names": participants,
			"tags":              []any{},
			"has_notes":         false,
		})
	}
	return rows
}

// mergeByLastTimestamp is the agent and board rows together, newest last_timestamp first.
func mergeByLastTimestamp(agentRows, boardRows []map[string]any) []map[string]any {
	merged := append(agentRows, boardRows...)
	sort.Slice(merged, func(i, j int) bool {
		ti, _ := merged[i]["last_timestamp"].(string)
		tj, _ := merged[j]["last_timestamp"].(string)
		// Normalize Z → +00:00 for consistent string comparison
		ti = strings.Replace(ti, "Z", "+00:00", 1)
		tj = strings.Replace(tj, "Z", "+00:00", 1)
		return ti > tj
	})
	return merged
}

// pageRows is page (from 1) of rows, pageSize to a page.
func pageRows(rows []map[string]any, page, pageSize int) []map[string]any {
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(rows) {
		start = len(rows)
	}
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end]
}

// GetSessionNotes returns notes for a historical session.
// GET /api/sessions/history/{sessionID}/notes
func (h *HistoryHandler) GetSessionNotes(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	meta, err := h.ss.GetSessionNotes(r.Context(), sid)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// Match Python response shape: include summarizing field, omit session_id
	resp := map[string]any{
		"notes_md":       meta.NotesMD,
		"auto_summary":   meta.AutoSummary,
		"is_user_edited": meta.IsUserEdited,
	}
	if meta.NotesMD == "" && meta.AutoSummary == "" {
		resp["summarizing"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// SaveSessionNotes saves notes for a historical session.
// PUT /api/sessions/history/{sessionID}/notes
func (h *HistoryHandler) SaveSessionNotes(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	var body struct {
		NotesMD string `json:"notes_md"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if err := h.ss.SaveSessionNotes(r.Context(), sid, body.NotesMD); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Resummarize runs synchronous summarization for a session.
// POST /api/sessions/history/{sessionID}/resummarize
func (h *HistoryHandler) Resummarize(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")

	if h.summarizeFn != nil {
		if err := h.summarizeFn(r.Context(), sid); err != nil {
			errInternalServer(w, err.Error())
			return
		}
	}

	// Return the (now-populated) auto_summary, falling back to notes_md
	// when user has edited notes (matches Python: returns notes_md as summary)
	meta, _ := h.ss.GetSessionNotes(r.Context(), sid)
	autoSummary := ""
	if meta != nil {
		autoSummary = meta.AutoSummary
		if autoSummary == "" && meta.NotesMD != "" {
			autoSummary = meta.NotesMD
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auto_summary": autoSummary})
}

// GetSessionTags returns tags for a session.
// GET /api/sessions/history/{sessionID}/tags
func (h *HistoryHandler) GetSessionTags(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	tags, err := h.ss.GetSessionTags(r.Context(), sid)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tags))
}

// GetSessionGit returns git snapshots for a historical session.
// GET /api/sessions/history/{sessionID}/git
func (h *HistoryHandler) GetSessionGit(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	limit := clampLimit(queryInt(r, "limit", 20), 20, 200)
	snaps, err := h.gs.GetGitSnapshotsForSession(r.Context(), sid, limit)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "commits": emptyIfNil(snaps)})
}

// GetSessionEvents returns events for a historical session, newest first; with type, q, since, until, sort or cursor it is a search, with
// the reply and rules of the live events route (total, next_cursor; a Coral Hub viewer matches on what it sees).
// GET /api/sessions/history/{sessionID}/events
func (h *HistoryHandler) GetSessionEvents(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	vals, perr := url.ParseQuery(r.URL.RawQuery)
	if perr != nil {
		errBadRequest(w, "the query string is not valid (no ';', and %-escapes must be two hex digits)")
		return
	}
	if store.QueriesEvents(vals) { // type, q, since, until, sort or cursor: a search, as on the live route (#4779)
		queryAgentEvents(w, r, h.ts, "", &sid, vals, historyEventQueryParamNames)
		return
	}
	limit := clampLimit(queryInt(r, "limit", 200), 200, 200)
	events, err := h.ts.ListAgentEvents(r.Context(), "", limit, &sid)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(events))
}

// GetSessionTasks returns tasks for a historical session.
// GET /api/sessions/history/{sessionID}/tasks
func (h *HistoryHandler) GetSessionTasks(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	tasks, err := h.ts.ListTasksBySession(r.Context(), sid)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tasks))
}

// sessionTranscript reads the whole transcript of a session by its id, live or ended, from this machine: the messages (nil when there is none) and the agent
// type the history index or the live row says (a Codex or Agy transcript is found by its Coral marker). The history view and the saved-message read use it.
func (h *HistoryHandler) sessionTranscript(ctx context.Context, sid string) (messages []map[string]any, agentType string) {
	agentType = "claude"
	workingDir := ""

	// The history index is authoritative for provider identity. A session ID
	// can be a Coral marker (Codex/Antigravity) rather than the native file ID;
	// defaulting to Claude first can pin the wrong reader cache and hide the
	// transcript from the history view.
	if h.ss != nil {
		if idx, err := h.ss.GetIndexedSession(ctx, sid); err == nil && idx != nil && idx.SourceType != "" {
			agentType = idx.SourceType
		}
	}

	if h.ss != nil {
		if ls, err := h.ss.GetLiveSession(ctx, sid); err == nil && ls != nil {
			if ls.AgentType != "" {
				agentType = ls.AgentType
			}
			if ls.WorkingDir != "" {
				workingDir = ls.WorkingDir
			}
		}
	}

	// Each request reads through its own reader (#3651). The reader is incremental: one shared
	// by concurrent requests hands the transcript to whichever reads first, and the others find
	// no new messages and answered 404 (or cleared the cache under each other's fallbacks).
	// A history view needs the whole transcript once, so nothing is worth caching between
	// requests here.
	rd := jsonl.NewSessionReader()
	messages, _ = rd.ReadNewMessages(sid, workingDir, agentType)
	if len(messages) == 0 && agentType != "claude" {
		// Fallback to claude
		rd.ClearSession(sid)
		messages, _ = rd.ReadNewMessages(sid, workingDir, "claude")
	}
	if len(messages) == 0 {
		// Try Codex, whose transcript filename uses its native session ID and
		// therefore may need the embedded Coral session marker for lookup.
		rd.ClearSession(sid)
		messages, _ = rd.ReadNewMessages(sid, workingDir, "codex")
		if len(messages) > 0 {
			agentType = "codex"
		}
	}
	if len(messages) == 0 {
		// Try Agy / Antigravity as fallback.
		rd.ClearSession(sid)
		messages, _ = rd.ReadNewMessages(sid, workingDir, "agy")
		if len(messages) > 0 {
			agentType = "agy"
		}
	}
	return messages, agentType
}

// GetSessionDetail returns all messages for a historical session.
// GET /api/sessions/history/{sessionID}
func (h *HistoryHandler) GetSessionDetail(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	messages, agentType := h.sessionTranscript(r.Context(), sid)
	if messages == nil {
		errNotFound(w, "Session '"+sid+"' not found")
		return
	}

	if key := r.URL.Query().Get("find_key"); key != "" {
		writeKeyLookup(w, r.Context(), messageKeySecretFor(h.cfg), &jsonl.Transcript{Window: messages, Total: len(messages)}, key) // Save for Later: is that message still in the transcript, and where (#4510)
		return
	}

	// A page (#3718): ?limit=N&offset=M is the N messages ending M before the newest, as the
	// live chat pages, with the total and the page's first index. Through Coral Hub a JSON reply
	// is held whole to be checked for secrets, and one over 2 MiB is refused, so the dashboard
	// asks for pages; without a limit the whole transcript, as before.
	total, start := len(messages), 0
	if limit, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && limit > 0 {
		limit = min(limit, 500) // a page is at most 500 messages (#4602)
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		end := max(total-max(offset, 0), 0)
		start = max(end-limit, 0)
		messages = messages[start:end]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sid,
		"agent_type": agentType,
		"messages":   withKeys(messageKeySecretFor(h.cfg), messages), // each message's stable key, for Save for Later (#4510)
		"total":      total,
		"start":      start,
	})
}

// GetSessionAgentNotes returns agent notes for a historical session.
// GET /api/sessions/history/{sessionID}/agent-notes
func (h *HistoryHandler) GetSessionAgentNotes(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	notes, err := h.ts.ListNotesBySession(r.Context(), sid)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(notes))
}

// GetSessionFiles returns files edited by agents during a historical session.
// GET /api/sessions/history/{sessionID}/files
func (h *HistoryHandler) GetSessionFiles(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")

	// Get working directory from git snapshot for path normalization
	workdir := ""
	if snap, err := h.gs.GetLatestGitStateBySession(r.Context(), sid); err == nil && snap != nil {
		workdir = snap.WorkingDirectory
	}

	edits, err := h.ts.GetFileEdits(r.Context(), sid, workdir)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	files := make([]store.ChangedFile, 0, len(edits))
	for fp, agents := range edits {
		files = append(files, store.ChangedFile{
			Filepath: fp,
			Status:   "agent_only",
			Agents:   agents,
			Source:   "agent_events",
		})
	}

	// Sort by filepath for stable output
	sort.Slice(files, func(i, j int) bool {
		return files[i].Filepath < files[j].Filepath
	})

	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "files": hubVisibleFiles(r, files)})
}

// GetResumeInfo returns metadata needed to resume a historical session.
// GET /api/sessions/history/{sessionID}/resume-info
func (h *HistoryHandler) GetResumeInfo(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	result := map[string]any{"session_id": sid}
	if h.ss != nil {
		if idx, err := h.ss.GetIndexedSession(r.Context(), sid); err == nil && idx != nil {
			if idx.SourceType != "" {
				result["agent_type"] = idx.SourceType
			}
			if nativeID := nativeResumeID(idx.SourceType, idx.SourceFile, sid); nativeID != "" {
				result["resume_session_id"] = nativeID
			}
		}
	}

	if ls, err := h.ss.GetLiveSession(r.Context(), sid); err == nil && ls != nil {
		result["working_dir"] = ls.WorkingDir
		result["agent_type"] = ls.AgentType
		result["board_name"] = ptr.Value(ls.BoardName)
		result["display_name"] = ptr.Value(ls.DisplayName)
	}

	writeJSON(w, http.StatusOK, result)
}

func nativeResumeID(agentType, sourceFile, fallback string) string {
	switch agentType {
	case "codex":
		base := filepath.Base(sourceFile)
		return strings.TrimSuffix(base, filepath.Ext(base))
	case "agy", "antigravity", "gemini":
		// Native Antigravity transcripts live at <brain>/<conversation>/
		// .system_generated/logs/transcript.jsonl. Legacy Gemini IDs are
		// already represented by the indexed session ID.
		if strings.HasSuffix(filepath.ToSlash(sourceFile), "/.system_generated/logs/transcript.jsonl") {
			logs := filepath.Dir(sourceFile)
			return filepath.Base(filepath.Dir(filepath.Dir(logs)))
		}
	}
	return fallback
}
