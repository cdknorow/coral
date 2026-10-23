package routes

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/sessionstate"
	"github.com/subgentic/coral/internal/store"
)

// ── Tasks ───────────────────────────────────────────────────────────────

func (h *SessionsHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)
	tasks, err := h.ts.ListAgentTasks(r.Context(), name, sidPtr)
	if err != nil || tasks == nil {
		tasks = []store.AgentTask{}
	}
	// A personal task belongs to its exact session, not the workspace/folder
	// name. Resolve the current or persisted display name by session ID so a
	// team task table cannot label every personal row with the workspace name.
	if sidPtr != nil && *sidPtr != "" {
		if display, ok := h.sessionDisplayName(r.Context(), *sidPtr); ok {
			for i := range tasks {
				tasks[i].DisplayName = &display
			}
		}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *SessionsHandler) sessionDisplayName(ctx context.Context, sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	if live, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && live != nil && live.DisplayName != nil && strings.TrimSpace(*live.DisplayName) != "" {
		return strings.TrimSpace(*live.DisplayName), true
	}
	if saved, err := h.ss.GetDisplayName(ctx, sessionID); err == nil && saved != nil && strings.TrimSpace(*saved) != "" {
		return strings.TrimSpace(*saved), true
	}
	return "", false
}

func (h *SessionsHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title     string             `json:"title"`
		Body      string             `json:"body"`
		Priority  string             `json:"priority"`
		SessionID string             `json:"session_id"`
		Workflow  board.TaskWorkflow `json:"workflow"`
		BlockedBy json.RawMessage    `json:"blocked_by"`
		Draft     bool               `json:"draft"`
		// Notify tells the agent to claim it (a short prompt typed into its
		// terminal; see claimPrompt).
		Notify bool `json:"notify"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "title is required")
		return
	}
	// A title is one line (it is shown in lists and prompts)
	body.Title = oneLineTitle(body.Title)
	if body.Title == "" {
		errBadRequest(w, "title is required")
		return
	}
	// Whose list it goes on (#3498): an agent's own, or the path's for the Operator.
	// A notify types a claim prompt into that agent, so the path can't redirect it.
	claim := body.SessionID
	if claim == "" {
		claim = queryClaim(r)
	}
	scope, ok := h.itemScope(w, r, claim)
	if !ok {
		return
	}
	name := scope.name
	if scope.sid != nil {
		body.SessionID = *scope.sid
	}

	// One row per open task: the operator's task and the agent's own
	// TaskCreate for it (synced by title) converge instead of duplicating.
	if existing, err := h.ts.FindOpenAgentTask(r.Context(), name, body.Title, ptr.NonEmpty(body.SessionID)); err == nil && existing != nil {
		writeJSON(w, http.StatusOK, existing)
		return
	}

	// Resolve display_name from live session
	var displayName *string
	var dn string
	err := h.db.Reader().GetContext(r.Context(), &dn,
		"SELECT display_name FROM live_sessions WHERE agent_name = ? AND status = 'active' AND display_name IS NOT NULL LIMIT 1", name)
	if err == nil && dn != "" {
		displayName = &dn
	}

	var deps []board.TaskDep
	if len(body.BlockedBy) > 0 {
		deps, err = parseBlockedBy(body.BlockedBy, store.AgentTaskProject(name, ptr.NonEmpty(body.SessionID)))
		if err != nil {
			errBadRequest(w, err.Error())
			return
		}
	}
	task, err := h.ts.CreateAgentTaskWithWorkflow(r.Context(), name, body.Title, ptr.NonEmpty(body.SessionID), displayName, body.Body, body.Priority, &board.CreateTaskOpts{Workflow: body.Workflow, BlockedBy: deps, Draft: body.Draft, MaxDepth: 32})
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	resp := createdTaskResponse{AgentTask: task}
	if body.Notify && task.Status == "pending" {
		resp.Notified, resp.NotifyError = h.notifyTaskCreated(r.Context(), name, body.SessionID, task)
	}
	writeJSON(w, http.StatusOK, resp)
}

// itemScope is whose tasks and notes a live-session route may create or change
// (#3498, security sweep item 9). claimed is the session the request names (its
// ?session_id=, or a create's body session_id).
//   - An agent, by its session token, acts only on its own: its agent name and session
//     come from the token, whatever the path says (the path's name comes from the
//     hook's working folder, which can drift: #3495's rule); claiming another session
//     is 403.
//   - A terminal session with no token (default mode only, the operator's rule as in
//     #3495) acts as the session it names, under that session's own agent name.
//   - The Operator (the dashboard, the verified CLI, the Hub's stamp) acts on the
//     path's {name}, and on the claimed session when one is given.
//   - Anyone else is 401 (callerSession).
//
// The GET list routes are not identity-bound (reads; #3475's design).
type itemScope struct {
	name  string
	sid   *string
	agent bool // an agent (or a terminal session) acting for itself
}

func (h *SessionsHandler) itemScope(w http.ResponseWriter, r *http.Request, claimed string) (itemScope, bool) {
	sid, ok := callerSession(w, r, claimed, true, h.isTerminalSession(r.Context()))
	if !ok {
		return itemScope{}, false
	}
	agentName := func(sessionID, fallback string) string {
		if ls, err := h.ss.GetLiveSession(r.Context(), sessionID); err == nil && ls != nil {
			return ls.AgentName
		}
		return fallback
	}
	if owner, isAgent := VerifiedSession(r.Context()); isAgent {
		return itemScope{name: agentName(owner.SessionID, owner.SessionName), sid: &sid, agent: true}, true
	}
	if !humanInputCaller(r) { // a terminal session, by the session it names
		return itemScope{name: agentName(sid, chi.URLParam(r, "name")), sid: &sid, agent: true}, true
	}
	return itemScope{name: chi.URLParam(r, "name"), sid: ptr.NonEmpty(claimed)}, true
}

// queryClaim is the session a request names in its query, or "".
func queryClaim(r *http.Request) string {
	return r.URL.Query().Get("session_id")
}

// owns reports whether an item of agent itemName and session itemSID is in scope: the
// same agent, and the same session when one is known (the list routes' rule). An item
// with no session (from before sessions were recorded) is its agent's.
func (s itemScope) owns(itemName string, itemSID *string) bool {
	if itemName != s.name {
		return false
	}
	if s.sid == nil {
		return true
	}
	if itemSID == nil {
		return s.agent
	}
	return *itemSID == *s.sid
}

// ownTask returns the task, or answers 404 when it isn't in scope.
func (h *SessionsHandler) ownTask(w http.ResponseWriter, r *http.Request, scope itemScope, taskID int64) (*store.AgentTask, bool) {
	t, err := h.ts.GetAgentTask(r.Context(), taskID)
	if err != nil {
		errInternalServer(w, err.Error())
		return nil, false
	}
	if t == nil || !scope.owns(t.AgentName, t.SessionID) {
		errNotFound(w, "task not found")
		return nil, false
	}
	return t, true
}

// ownNote reports whether the note is in scope, answering 404 when it isn't.
func (h *SessionsHandler) ownNote(w http.ResponseWriter, r *http.Request, scope itemScope, noteID int64) bool {
	n, err := h.ts.GetAgentNote(r.Context(), noteID)
	if err != nil {
		errInternalServer(w, err.Error())
		return false
	}
	if n == nil || !scope.owns(n.AgentName, n.SessionID) {
		errNotFound(w, "note not found")
		return false
	}
	return true
}

func (h *SessionsHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	taskID, _ := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	scope, ok := h.itemScope(w, r, queryClaim(r))
	if !ok {
		return
	}
	if _, ok := h.ownTask(w, r, scope, taskID); !ok {
		return
	}
	var body struct {
		Title     *string `json:"title"`
		Completed *int    `json:"completed"`
		SortOrder *int    `json:"sort_order"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if err := h.ts.UpdateAgentTask(r.Context(), taskID, body.Title, body.Completed, body.SortOrder); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	h.scheduleTaskReadiness(taskID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SessionsHandler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	taskID, _ := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	scope, ok := h.itemScope(w, r, queryClaim(r))
	if !ok {
		return
	}
	if _, ok := h.ownTask(w, r, scope, taskID); !ok {
		return
	}
	if err := h.ts.DeleteAgentTask(r.Context(), taskID); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SessionsHandler) ReorderTasks(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.itemScope(w, r, queryClaim(r))
	if !ok {
		return
	}
	var body struct {
		TaskIDs []int64 `json:"task_ids"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.TaskIDs) == 0 {
		errBadRequest(w, "task_ids required")
		return
	}
	for _, id := range body.TaskIDs {
		if _, ok := h.ownTask(w, r, scope, id); !ok {
			return // nothing reordered: every id must be in scope
		}
	}
	if err := h.ts.ReorderAgentTasks(r.Context(), scope.name, body.TaskIDs); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Notes ───────────────────────────────────────────────────────────────

func (h *SessionsHandler) ListNotes(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)
	notes, err := h.ts.ListAgentNotes(r.Context(), name, sidPtr)
	if err != nil || notes == nil {
		notes = []store.AgentNote{}
	}
	writeJSON(w, http.StatusOK, notes)
}

func (h *SessionsHandler) CreateNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content   string `json:"content"`
		SessionID string `json:"session_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Content == "" {
		errBadRequest(w, "content is required")
		return
	}
	claim := body.SessionID
	if claim == "" {
		claim = queryClaim(r)
	}
	scope, ok := h.itemScope(w, r, claim) // whose notes (#3498)
	if !ok {
		return
	}
	name := scope.name
	if scope.sid != nil {
		body.SessionID = *scope.sid
	}
	note, err := h.ts.CreateAgentNote(r.Context(), name, body.Content, ptr.NonEmpty(body.SessionID))
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, note)
}

func (h *SessionsHandler) UpdateNote(w http.ResponseWriter, r *http.Request) {
	noteID, _ := strconv.ParseInt(chi.URLParam(r, "noteID"), 10, 64)
	scope, ok := h.itemScope(w, r, queryClaim(r))
	if !ok {
		return
	}
	if !h.ownNote(w, r, scope, noteID) {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Content == "" {
		errBadRequest(w, "content is required")
		return
	}
	if err := h.ts.UpdateAgentNote(r.Context(), noteID, body.Content); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SessionsHandler) DeleteNote(w http.ResponseWriter, r *http.Request) {
	noteID, _ := strconv.ParseInt(chi.URLParam(r, "noteID"), 10, 64)
	scope, ok := h.itemScope(w, r, queryClaim(r))
	if !ok {
		return
	}
	if !h.ownNote(w, r, scope, noteID) {
		return
	}
	if err := h.ts.DeleteAgentNote(r.Context(), noteID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Events ──────────────────────────────────────────────────────────────

// ListEvents: GET /api/sessions/live/{name}/events[?session_id=&limit=]: the newest events, a bare array. With type, q, since, until, sort or
// cursor it is the search of #4745 (agent_events_query.go): type (an event type, comma separated or repeated), q (a plain, case-insensitive
// substring of the summary or tool name, at most 200 characters), since (inclusive) and until (exclusive), sort=newest|oldest, limit 1 to 200
// and cursor; the reply is then {"events": [...], "total": N, "limit": N, "next_cursor": "...", "prev_cursor": "...", "truncated": true},
// paging both ways, with the same parameter names and reply shape as the other searches. An unknown or malformed parameter is a 400 naming it.
// Through Coral Hub the match is made on what the viewer sees, and a q holding a secret is refused.
func (h *SessionsHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)
	vals, perr := url.ParseQuery(r.URL.RawQuery)
	if perr != nil {
		errBadRequest(w, "the query string is not valid (no ';', and %-escapes must be two hex digits)")
		return
	}
	if store.QueriesEvents(vals) {
		h.queryEvents(w, r, name, sidPtr, vals)
		return
	}
	limit := clampLimit(queryInt(r, "limit", 50), 50, 200)
	events, err := h.ts.ListAgentEvents(r.Context(), name, limit, sidPtr)
	if err != nil || events == nil {
		events = []store.AgentEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *SessionsHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		EventType  string `json:"event_type"`
		Summary    string `json:"summary"`
		ToolName   string `json:"tool_name"`
		SessionID  string `json:"session_id"`
		DetailJSON any    `json:"detail_json"`
		DurationMs *int64 `json:"duration_ms"`
		ToolUseID  string `json:"tool_use_id"`
		HooksRev   int    `json:"hooks_rev"`
		// PermissionMode is the Claude CLI's current permission mode, from its hook payload (live_permission_mode.go)
		PermissionMode string `json:"permission_mode"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.EventType == "" || body.Summary == "" {
		errBadRequest(w, "event_type and summary required")
		return
	}
	// Who may post (#3479, Security): these events decide an agent's state, and from it
	// when Coral types into the agent (#3475), so an event is accepted only from that
	// agent's own hooks, identified by its session token, in every mode. The token alone
	// decides the session: a session_id naming another one is refused, a missing one is
	// filled in. The path's name is not checked; it comes from the hook's working folder
	// and can name another agent in the same folder, so the session's own name is used.
	// The one event the dashboard posts, a goal edited by the Operator, is also accepted
	// from the Operator. Coral Hub can't reach this route (TierNever).
	owner, hasToken := VerifiedSession(r.Context())
	switch {
	case hasToken:
		if body.SessionID != "" && body.SessionID != owner.SessionID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "identity_mismatch",
				"message": "An agent can post events only for its own session"})
			return
		}
		body.SessionID = owner.SessionID
	case body.EventType == "goal" && humanInputCaller(r):
	default:
		// Logged once per claimed session (an agent's hooks post several events a
		// minute), so an agent that predates session tokens is easy to spot.
		if claimed := body.SessionID + "|" + name; refusedEventSenders.add(claimed) {
			slog.Warn("agent events refused: no session token (restart the agent from Coral to restore its status)",
				"session", name, "session_id", body.SessionID, "event_type", body.EventType, "tool", r.UserAgent())
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session_token_required",
			"message": "Agent events come from the agent's own hooks, with its session token. Restart the agent from Coral if it predates session tokens."})
		return
	}
	if !postableEventTypes[body.EventType] {
		errBadRequest(w, "unknown event_type "+strconv.Quote(body.EventType))
		return
	}
	if hasToken && body.HooksRev > 0 {
		_ = h.ss.SetHooksRev(r.Context(), body.SessionID, body.HooksRev) // kept across restarts (#3665)
		// The agent's own hooks say which set they are. Only a revision is stored: an event
		// without one (an older set, or an old Coral hook left in the user's global Claude
		// settings next to the current ones) must never turn a current set back (Security's
		// SHOULD on #3655: flapping would spoil the shadow data).
		h.hooksRev.Store(body.SessionID, body.HooksRev)
	} else if hasToken && body.EventType != "goal" {
		h.hooksNoRev.Store(body.SessionID, true) // for the "out of date" log only
	}
	if len(body.Summary) > maxEventSummaryBytes {
		body.Summary = truncateUTF8(body.Summary, maxEventSummaryBytes)
	}
	if body.EventType == "tool_use" && body.DurationMs == nil && body.SessionID != "" {
		if ms, ok := h.pending.durationMs(body.SessionID, body.ToolUseID, time.Now()); ok {
			body.DurationMs = &ms
		}
	}

	// If session_id is provided, look up the actual agent name from the live
	// sessions DB. This is more reliable than the URL path name, which comes
	// from the hook's cwd and can be wrong when multiple agents share a directory.
	agentName := name
	if body.SessionID != "" {
		if ls, err := h.ss.GetLiveSession(r.Context(), body.SessionID); err == nil && ls != nil {
			agentName = ls.AgentName
			if hasToken {
				h.noteLiveMode(body.SessionID, ls.AgentType, body.PermissionMode) // from the agent's own hooks only
			}
		}
	}

	event := &store.AgentEvent{
		AgentName: agentName,
		EventType: body.EventType,
		Summary:   body.Summary,
	}
	if body.SessionID != "" {
		event.SessionID = &body.SessionID
	}
	if body.ToolName != "" {
		event.ToolName = &body.ToolName
	}
	if body.DurationMs != nil && *body.DurationMs >= 0 {
		event.DurationMs = body.DurationMs
	}
	if body.ToolUseID != "" {
		event.RefID = &body.ToolUseID
	}
	// A goal posted here without a source is the operator's edit; the goal
	// generator never replaces it.
	if body.EventType == "goal" && body.DetailJSON == nil {
		body.DetailJSON = map[string]string{"source": "user"}
	}
	// A request's kind is shown on the dashboard: only the known kinds are stored (#3481,
	// QA's review). The event comes from the agent's own hook, but its text is the agent's.
	if body.EventType == "permission_request" || body.EventType == "request_resolved" {
		if d, ok := body.DetailJSON.(map[string]any); ok {
			if k, _ := d["kind"].(string); !sessionstate.RequestKinds[k] {
				d["kind"] = ""
			}
		}
	}
	if body.DetailJSON != nil {
		djBytes, err := json.Marshal(body.DetailJSON)
		if err != nil {
			slog.Warn("failed to marshal detail_json", "error", err)
		} else if len(djBytes) > maxEventDetailBytes {
			errBadRequest(w, "detail_json is too large")
			return
		} else {
			s := string(djBytes)
			event.DetailJSON = &s
		}
	}

	created, err := h.ts.InsertAgentEvent(r.Context(), event)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	h.notePendingToolEvent(body.SessionID, body.EventType, body.ToolUseID)
	if hasToken && body.EventType == "session_end" {
		// The agent's process ended (Claude's SessionEnd): nothing held for it may be
		// typed into whatever runs in that terminal next (#3481). Not on SessionStart:
		// a launch's first prompt is a held send waiting for that very start.
		ptymanager.DropHeld(owner.SessionName, "the agent's session ended")
		h.liveModes.Delete(body.SessionID) // an ended session keeps no mode (and the map no id for the life of the process)
	}
	// Only the agent's own (token-verified) notification wakes auto-accept early; the sweep
	// itself acts only on the state derived from such events.
	if hasToken && sessionstate.Classify(body.Summary) == sessionstate.NotificationNeedsInput {
		h.autoAccept.Kick() // an auto-accept session's permission prompt is answered now, not on the next sweep
	}
	if hasToken && body.EventType == "stop" && h.indexKick != nil {
		h.indexKick() // the turn is done and its reply is in the transcript: History lists the conversation within seconds, not at the next index pass (#6198)
	}
	writeJSON(w, http.StatusOK, created)
}

// refusedEventSenders remembers which claimed sessions were already logged as refused
// (bounded: a flood of made-up names can't grow it without limit).
var refusedEventSenders = &onceSet{max: 1000}

type onceSet struct {
	mu   sync.Mutex
	seen map[string]bool
	max  int
}

// add reports whether key is new. Once max keys are remembered it reports false for
// every other key too, so the log stays quiet.
func (s *onceSet) add(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[key] || len(s.seen) >= s.max {
		return false
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	s.seen[key] = true
	return true
}

// postableEventTypes are the event types POST /api/sessions/live/{name}/events accepts
// (#3479): the ones Coral's hooks send, and the dashboard's goal edit. Others are refused,
// never mapped. Coral records its own events (auto_accept, retry, ...) directly.
var postableEventTypes = map[string]bool{
	"session_reset": true, "prompt_submit": true, "tool_use": true, "stop": true, "notification": true, "goal": true,
	// #3481: a request to the user opened (PermissionRequest, Elicitation) or answered
	// (ElicitationResult), and the agent's session ended.
	"permission_request": true, "request_resolved": true, "session_end": true,
}

// Limits on a posted event: a hook's summary is short and its detail a small JSON object
// (tool input previews, truncated responses).
const (
	maxEventSummaryBytes = 2000
	maxEventDetailBytes  = 64 << 10
)

func (h *SessionsHandler) EventCounts(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)
	counts, err := h.ts.GetAgentEventCounts(r.Context(), name, sidPtr)
	if err != nil || counts == nil {
		counts = []store.ToolCount{}
	}
	writeJSON(w, http.StatusOK, counts)
}

func (h *SessionsHandler) ClearEvents(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)
	claimed := ""
	if sidPtr != nil {
		claimed = *sidPtr
	}
	// Only an agent's own token (for its own session) or the Operator clears an
	// agent's events (#3495).
	sid, ok := callerSession(w, r, claimed, true, nil)
	if !ok {
		return
	}
	if owner, isAgent := VerifiedSession(r.Context()); isAgent {
		sidPtr = &sid
		name = owner.SessionName
		if ls, err := h.ss.GetLiveSession(r.Context(), sid); err == nil && ls != nil {
			name = ls.AgentName
		}
	}
	if err := h.ts.ClearAgentEvents(r.Context(), name, sidPtr); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RecordTokenUsage records a token usage snapshot for a session.
// POST /api/sessions/live/{name}/token-usage
func (h *SessionsHandler) RecordTokenUsage(w http.ResponseWriter, r *http.Request) {
	if h.tokenStore == nil {
		errInternalServer(w, "token tracking not available")
		return
	}

	name := chi.URLParam(r, "name")
	var body struct {
		SessionID        string  `json:"session_id"`
		InputTokens      float64 `json:"input_tokens"`
		OutputTokens     float64 `json:"output_tokens"`
		CacheReadTokens  float64 `json:"cache_read_tokens"`
		CacheWriteTokens float64 `json:"cache_write_tokens"`
		CostUSD          float64 `json:"cost_usd"`
		NumTurns         float64 `json:"num_turns"`
		// Model is optional. When the caller does not know it the row stores
		// NULL; the session's configured model is deliberately not substituted,
		// since it may differ from the model that produced these tokens.
		Model string `json:"model"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// Only the agent's own hook reports its usage (#3495): the token decides the
	// session, and its agent name is the session's, not the path's.
	sid, ok := callerSession(w, r, body.SessionID, false, nil)
	if !ok {
		return
	}
	body.SessionID = sid

	ctx := r.Context()

	// Resolve team_id and board_name from the live session
	var teamID *int64
	var boardName *string
	var agentType string
	if body.SessionID != "" {
		if ls, err := h.ss.GetLiveSession(ctx, body.SessionID); err == nil && ls != nil {
			if ls.TeamID != nil {
				teamID = ls.TeamID
			}
			if ls.BoardName != nil {
				boardName = ls.BoardName
			}
			agentType = ls.AgentType
			name = ls.AgentName
		}
	}
	if agentType == "" {
		agentType = "claude"
	}

	inputTokens := int(body.InputTokens)
	outputTokens := int(body.OutputTokens)
	cacheReadTokens := int(body.CacheReadTokens)
	cacheWriteTokens := int(body.CacheWriteTokens)

	err := h.tokenStore.RecordUsage(ctx, &store.TokenUsage{
		SessionID:        body.SessionID,
		AgentName:        name,
		AgentType:        agentType,
		TeamID:           teamID,
		BoardName:        boardName,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		TotalTokens:      inputTokens + outputTokens + cacheReadTokens + cacheWriteTokens,
		CostUSD:          body.CostUSD,
		NumTurns:         int(body.NumTurns),
		Model:            body.Model,
	})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
