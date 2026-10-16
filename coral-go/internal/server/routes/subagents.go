package routes

import (
	"errors"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/subagent"
)

// Subagent statuses reported to the UI. They reuse the task list's status
// vocabulary so subagent rows render with the same icons as tasks.
const (
	subagentStatusInProgress = "in_progress"
	subagentStatusCompleted  = "completed"
	// subagentStatusStopped: the subagent never returned a final answer and
	// its main agent is gone, so it never will.
	subagentStatusStopped = "skipped"
)

// subagentView is a stored subagent plus the status derived for display.
type subagentView struct {
	store.Subagent
	Status string `json:"status"`
}

// subagentStatus derives a display status. mainAgentStopped reports whether
// the main agent that launched the subagent has been stopped.
func subagentStatus(sa store.Subagent, mainAgentStopped bool) string {
	switch {
	case sa.Finished:
		return subagentStatusCompleted
	case mainAgentStopped:
		// A subagent runs inside its main agent's process. Without this, one
		// interrupted mid-run would show a spinner forever.
		return subagentStatusStopped
	default:
		return subagentStatusInProgress
	}
}

// ListSubagents returns the subagents launched by one main agent, with their
// spend. The session is identified by the session_id query parameter, and {name}
// must be that session's agent name or its "<type>-<id>" session name (#3870),
// as for one subagent; otherwise 404.
// GET /api/sessions/live/{name}/subagents?session_id=<id>
func (h *SessionsHandler) ListSubagents(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	out := []subagentView{}
	if sid == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}

	ls, ok := h.namedSubagentSession(w, r, sid)
	if !ok {
		return
	}
	rows, err := h.subagents.ListSubagentsBySession(r.Context(), sid)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	mainAgentStopped := ls.StoppedAt != nil
	for _, sa := range rows {
		out = append(out, subagentView{Subagent: sa, Status: subagentStatus(sa, mainAgentStopped)})
	}
	writeJSON(w, http.StatusOK, out)
}

// subagentDetail is a subagent plus the two ends of its conversation.
type subagentDetail struct {
	subagentView
	subagent.Conversation
	// ConversationAvailable is false when the transcript can no longer be read
	// (for example Claude Code pruned it); the stored stats are still returned.
	ConversationAvailable bool `json:"conversation_available"`
}

// GetSubagent returns one subagent with the prompt it was given and the
// answer it returned, read from its transcript on demand.
// GET /api/sessions/live/{name}/subagents/{subagentID}?session_id=<id>
func (h *SessionsHandler) GetSubagent(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	subagentID := chi.URLParam(r, "subagentID")
	if sid == "" {
		errBadRequest(w, "session_id is required")
		return
	}

	ls, ok := h.namedSubagentSession(w, r, sid)
	if !ok {
		return
	}
	sa, err := h.subagents.GetSubagent(r.Context(), sid, subagentID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if sa == nil {
		errNotFound(w, "subagent not found")
		return
	}

	mainAgentStopped := ls.StoppedAt != nil
	out := subagentDetail{subagentView: subagentView{Subagent: *sa, Status: subagentStatus(*sa, mainAgentStopped)}}

	// The path is never taken from the request: it is the one the poller
	// recorded when it found the transcript.
	if sa.TranscriptPath != nil && *sa.TranscriptPath != "" {
		conv, err := subagent.ReadConversation(*sa.TranscriptPath)
		switch {
		case err == nil:
			out.Conversation = *conv
			out.ConversationAvailable = true
		case errors.Is(err, os.ErrNotExist):
			// Stats are still worth returning without the conversation.
		default:
			errInternalServer(w, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// namedSubagentSession is the session sid when the path's {name} is its agent name or its
// "<type>-<id>" session name (#3870, Security's sweep #3867); otherwise it answers 404 (the
// body a missing subagent gets) and reports false, so a subagent is never read under another
// agent's name.
func (h *SessionsHandler) namedSubagentSession(w http.ResponseWriter, r *http.Request, sid string) (*store.LiveSession, bool) {
	ls, err := h.ss.GetLiveSession(r.Context(), sid)
	if err != nil {
		errInternalServer(w, err.Error())
		return nil, false
	}
	name := chi.URLParam(r, "name")
	if ls == nil || (name != ls.AgentName && name != naming.SessionName(ls.AgentType, ls.SessionID)) {
		errNotFound(w, "subagent not found")
		return nil, false
	}
	return ls, true
}
