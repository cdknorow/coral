package routes

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/jsonl"
	"github.com/cdknorow/coral/internal/store"
)

// HistoryHandler handles session history / search endpoints.
type HistoryHandler struct {
	ss          *store.SessionStore
	ts          *store.TaskStore
	gs          *store.GitStore
	bs          *board.Store
	cfg         *config.Config
	jsonl       *jsonl.SessionReader
	summarizeFn func(ctx context.Context, sessionID string) error
}

// SetSummarizeFn sets the function used for synchronous summarization.
func (h *HistoryHandler) SetSummarizeFn(fn func(ctx context.Context, sessionID string) error) {
	h.summarizeFn = fn
}

func NewHistoryHandler(db *store.DB, cfg *config.Config, bs *board.Store) *HistoryHandler {
	return &HistoryHandler{
		ss:    store.NewSessionStore(db),
		ts:    store.NewTaskStore(db),
		gs:    store.NewGitStore(db),
		bs:    bs,
		cfg:   cfg,
		jsonl: jsonl.NewSessionReader(),
	}
}

// ListSessions returns paginated, filtered history sessions.
// GET /api/sessions/history
func (h *HistoryHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
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
		sourceTypes = strings.Split(raw, ",")
	}

	var minDur, maxDur *int
	if v := q.Get("min_duration_sec"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			minDur = &n
		}
	}
	if v := q.Get("max_duration_sec"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxDur = &n
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
		MinDurationSec: minDur,
		MaxDurationSec: maxDur,
		AgentName:      q.Get("agent_name"),
	}

	// Chat type filter: all, agent, group
	chatType := q.Get("type")
	if chatType != "agent" && chatType != "group" {
		chatType = "all"
	}

	// Check if any filters are active (used for cold-start fast path)
	hasAnyFilter := params.Search != "" || len(params.TagIDs) > 0 ||
		len(params.SourceTypes) > 0 || params.DateFrom != "" || params.DateTo != "" ||
		params.MinDurationSec != nil || params.MaxDurationSec != nil ||
		params.AgentName != ""

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
		if result.Total == 0 && !hasAnyFilter && chatType != "group" {
			writeJSON(w, http.StatusOK, map[string]any{
				"sessions":  agentSessions,
				"total":     0,
				"page":      1,
				"page_size": 0,
			})
			return
		}

		agentTotal = result.Total
		for _, s := range result.Sessions {
			m := map[string]any{
				"session_id":      s.SessionID,
				"source_type":     s.SourceType,
				"first_timestamp": derefStrPtr(s.FirstTimestamp),
				"last_timestamp":  derefStrPtr(s.LastTimestamp),
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
			}
			agentSessions = append(agentSessions, m)
		}
	}

	// ── Group chats (board projects) ──
	var boardSessions []map[string]any
	if (chatType == "all" || chatType == "group") && h.bs != nil {
		projects, err := h.bs.ListProjectsEnriched(r.Context())
		if err == nil {
			searchQ := q.Get("q")
			dateFrom := q.Get("date_from")
			dateTo := q.Get("date_to")

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
				cutoff := dateTo + "T23:59:59"
				filtered := projects[:0]
				for _, p := range projects {
					if p.FirstMessageAt != nil && *p.FirstMessageAt <= cutoff {
						filtered = append(filtered, p)
					}
				}
				projects = filtered
			}

			for _, p := range projects {
				participants := ""
				if p.ParticipantNames != nil {
					participants = *p.ParticipantNames
				}
				boardSessions = append(boardSessions, map[string]any{
					"session_id":        fmt.Sprintf("board:%s", p.Project),
					"title":             p.Project,
					"type":              "group",
					"source_type":       "board",
					"summary":           fmt.Sprintf("%d messages, %d participants", p.MessageCount, p.SubscriberCount),
					"first_timestamp":   derefStrPtr(p.FirstMessageAt),
					"last_timestamp":    derefStrPtr(p.LastMessageAt),
					"message_count":     p.MessageCount,
					"subscriber_count":  p.SubscriberCount,
					"participant_names": participants,
					"tags":              []any{},
					"has_notes":         false,
				})
			}
		}
	}

	// ── Merge and return ──
	if chatType == "agent" {
		writeJSON(w, http.StatusOK, map[string]any{
			"sessions":  agentSessions,
			"total":     agentTotal,
			"page":      page,
			"page_size": pageSize,
		})
		return
	}

	if chatType == "group" {
		total := len(boardSessions)
		start := (page - 1) * pageSize
		end := start + pageSize
		if start > total {
			start = total
		}
		if end > total {
			end = total
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sessions":  boardSessions[start:end],
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		})
		return
	}

	// type == "all": merge agent + board, sorted by last_timestamp desc
	merged := append(agentSessions, boardSessions...)
	sort.Slice(merged, func(i, j int) bool {
		ti, _ := merged[i]["last_timestamp"].(string)
		tj, _ := merged[j]["last_timestamp"].(string)
		// Normalize Z → +00:00 for consistent string comparison
		ti = strings.Replace(ti, "Z", "+00:00", 1)
		tj = strings.Replace(tj, "Z", "+00:00", 1)
		return ti > tj
	})
	total := agentTotal + len(boardSessions)
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(merged) {
		start = len(merged)
	}
	if end > len(merged) {
		end = len(merged)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":  merged[start:end],
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
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
	limit := queryInt(r, "limit", 20)
	snaps, err := h.gs.GetGitSnapshotsForSession(r.Context(), sid, limit)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "commits": emptyIfNil(snaps)})
}

// GetSessionEvents returns events for a historical session.
// GET /api/sessions/history/{sessionID}/events
func (h *HistoryHandler) GetSessionEvents(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	limit := queryInt(r, "limit", 200)
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

// GetSessionDetail returns all messages for a historical session.
// GET /api/sessions/history/{sessionID}
func (h *HistoryHandler) GetSessionDetail(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")

	// Use the JSONL reader to load messages. Pass empty working dir
	// so it searches all project directories for the session file.
	messages, _ := h.jsonl.ReadNewMessages(sid, "", "claude")
	if len(messages) == 0 {
		// Try Codex, whose transcript filename uses its native session ID and
		// therefore may need the embedded Coral session marker for lookup.
		h.jsonl.ClearSession(sid)
		messages, _ = h.jsonl.ReadNewMessages(sid, "", "codex")
	}
	if len(messages) == 0 {
		// Try Gemini as fallback.
		h.jsonl.ClearSession(sid)
		messages, _ = h.jsonl.ReadNewMessages(sid, "", "gemini")
	}
	if messages == nil {
		errNotFound(w, "Session '"+sid+"' not found")
		return
	}

	// Clear cached state so the reader doesn't hold stale history data
	defer h.jsonl.ClearSession(sid)

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sid,
		"messages":   messages,
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
			Source:    "agent_events",
		})
	}

	// Sort by filepath for stable output
	sort.Slice(files, func(i, j int) bool {
		return files[i].Filepath < files[j].Filepath
	})

	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "files": files})
}


// GetResumeInfo returns metadata needed to resume a historical session.
// GET /api/sessions/history/{sessionID}/resume-info
func (h *HistoryHandler) GetResumeInfo(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	result := map[string]any{"session_id": sid}

	if ls, err := h.ss.GetLiveSession(r.Context(), sid); err == nil && ls != nil {
		result["working_dir"] = ls.WorkingDir
		result["agent_type"] = ls.AgentType
		result["board_name"] = derefStrPtr(ls.BoardName)
		result["display_name"] = derefStrPtr(ls.DisplayName)
	}

	writeJSON(w, http.StatusOK, result)
}
