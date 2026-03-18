package routes

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
)

// HistoryHandler handles session history / search endpoints.
type HistoryHandler struct {
	ss  *store.SessionStore
	ts  *store.TaskStore
	gs  *store.GitStore
	cfg *config.Config
}

func NewHistoryHandler(db *store.DB, cfg *config.Config) *HistoryHandler {
	return &HistoryHandler{
		ss:  store.NewSessionStore(db),
		ts:  store.NewTaskStore(db),
		gs:  store.NewGitStore(db),
		cfg: cfg,
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
	}

	result, err := h.ss.ListSessionsPaged(r.Context(), params)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GetSessionNotes returns notes for a historical session.
// GET /api/sessions/{sessionID}/notes
func (h *HistoryHandler) GetSessionNotes(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	meta, err := h.ss.GetSessionNotes(r.Context(), sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

// SaveSessionNotes saves notes for a historical session.
// PUT /api/sessions/{sessionID}/notes
func (h *HistoryHandler) SaveSessionNotes(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	var body struct {
		NotesMD string `json:"notes_md"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if err := h.ss.SaveSessionNotes(r.Context(), sid, body.NotesMD); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Resummarize re-queues a session for AI summarization.
// POST /api/sessions/{sessionID}/resummarize
func (h *HistoryHandler) Resummarize(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	if err := h.ss.EnqueueForSummarization(r.Context(), sid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GetSessionTags returns tags for a session.
// GET /api/sessions/{sessionID}/tags
func (h *HistoryHandler) GetSessionTags(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	tags, err := h.ss.GetSessionTags(r.Context(), sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if tags == nil {
		tags = []store.Tag{}
	}
	writeJSON(w, http.StatusOK, tags)
}

// GetSessionGit returns git snapshots for a historical session.
// GET /api/sessions/{sessionID}/git
func (h *HistoryHandler) GetSessionGit(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	limit := queryInt(r, "limit", 20)
	snaps, err := h.gs.GetGitSnapshotsForSession(r.Context(), sid, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sid, "snapshots": snaps})
}

// GetSessionEvents returns events for a historical session.
// GET /api/sessions/{sessionID}/events
func (h *HistoryHandler) GetSessionEvents(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	limit := queryInt(r, "limit", 200)
	events, err := h.ts.ListAgentEvents(r.Context(), "", limit, &sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// GetSessionTasks returns tasks for a historical session.
// GET /api/sessions/{sessionID}/tasks
func (h *HistoryHandler) GetSessionTasks(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sessionID")
	tasks, err := h.ts.ListTasksBySession(r.Context(), sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}
