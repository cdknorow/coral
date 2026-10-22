package routes

import (
	"context"
	"log/slog"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/store"
)

// Moving agents onto the current hook set (#3665). Claude reads its hooks once, at launch, so an
// agent started before a hook-set change keeps the old one until it is relaunched. Coral records
// the set per session (live_sessions.hooks_rev): at every Claude launch, restart, resume and wake
// (the set Coral just installed: authoritative, no event needed), and from the revision the
// agent's own hook events carry. The dashboard lists the agents on an older set, or not heard
// from since Coral started, and resumes each through Sleep then Wake: the same session, its
// conversation, its stored flags and permission mode, its worktree and its board.

// noteLaunchedHooks records that a Claude agent was just started with the current hook set.
func (h *SessionsHandler) noteLaunchedHooks(ctx context.Context, sessionID, agentType string) {
	h.noteLaunchedRunTag(ctx, sessionID)  // a tmux agent's tag, for Kill to find what detaches (#6141)
	h.noteLaunchedHookDir(ctx, sessionID) // every agent type's hooks are found on the PATH it was started with (#6089)
	if agentType != at.Claude || sessionID == "" || h.ss == nil {
		return
	}
	if err := h.ss.SetHooksRev(ctx, sessionID, agent.ClaudeHooksRev); err != nil {
		slog.Warn("could not record an agent's hook set", "session_id", sessionID, "error", err)
	}
}

// Hook-set statuses of a running Claude agent.
const (
	hooksCurrent = "current"
	hooksOlder   = "older"   // a lower revision, or events without one (before v1.4.53)
	hooksUnknown = "unknown" // nothing recorded and no event since this server started
)

// hooksRevOf is the newest hook-set revision known for a session: the recorded one (the column, set
// at every launch Coral made) or what its events said since this server started. 0 means none.
func (h *SessionsHandler) hooksRevOf(sessionID string, recorded map[string]int) int {
	rev := recorded[sessionID]
	if v, ok := h.hooksRev.Load(sessionID); ok {
		if n, _ := v.(int); n > rev {
			rev = n
		}
	}
	return rev
}

// hooksStatusOf classifies one session: the recorded revision (the column, or what its events
// said since this server started), else whether its events carried none.
func (h *SessionsHandler) hooksStatusOf(sessionID string, recorded map[string]int) string {
	rev := h.hooksRevOf(sessionID, recorded)
	switch {
	case rev >= agent.ClaudeHooksRev:
		return hooksCurrent
	case rev > 0:
		return hooksOlder
	}
	if _, noRev := h.hooksNoRev.Load(sessionID); noRev {
		return hooksOlder
	}
	return hooksUnknown
}

// A status says what Coral can tell: "older" with a rev is a recorded lower revision; "older" without
// one is an agent whose events carry no revision (a hook set before they did); "unknown" is none of
// these: no revision recorded and no event heard since this server started (so it may well be current).
type hooksAgent struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Team      string `json:"team,omitempty"`
	Status    string `json:"status"`
	Rev       int    `json:"rev,omitempty"` // the revision it runs, when one is recorded; the current one is "current"
}

// HooksStatus lists the running Claude agents that aren't on the current hook set, by team.
// GET /api/system/hooks-status (Operator only; Launch through Coral Hub)
func (h *SessionsHandler) HooksStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessions, err := h.ss.GetAllLiveSessions(ctx)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	recorded, err := h.ss.HooksRevs(ctx)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	agents := []hooksAgent{}
	for _, ls := range sessions {
		if ls.AgentType != at.Claude || ls.IsSleeping == 1 || ls.IsArchived == 1 || ls.IsJob == 1 {
			continue // a sleeping agent loads the current set when it is woken
		}
		if st := h.hooksStatusOf(ls.SessionID, recorded); st != hooksCurrent {
			name := ptr.Value(ls.DisplayName)
			if name == "" {
				name = ls.AgentName
			}
			agents = append(agents, hooksAgent{SessionID: ls.SessionID, Name: name, Team: ptr.Value(ls.BoardName), Status: st, Rev: h.hooksRevOf(ls.SessionID, recorded)})
		}
	}
	sort.SliceStable(agents, func(i, j int) bool {
		if agents[i].Team != agents[j].Team {
			return agents[i].Team < agents[j].Team
		}
		return agents[i].Name < agents[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"current": agent.ClaudeHooksRev, "agents": agents, "missing": h.agentsWithoutHooks(ctx, sessions)})
}

// agentsWithoutHooks lists the running agents, of any type, whose hook programs are gone from the folder they were started with (#6089): the app was replaced or moved while they ran. The
// dashboard flags each once on its row; resuming it (Sleep then Wake) starts it with the tools that are there now.
func (h *SessionsHandler) agentsWithoutHooks(ctx context.Context, sessions []store.LiveSession) []hooksAgent {
	out := []hooksAgent{}
	dirs, err := h.ss.HookDirs(ctx)
	if err != nil || len(dirs) == 0 {
		return out
	}
	gone := map[string]bool{} // one look at each folder, however many agents share it
	for _, ls := range sessions {
		if ls.IsSleeping == 1 || ls.IsArchived == 1 || ls.IsJob == 1 {
			continue
		}
		dir := dirs[ls.SessionID]
		if dir == "" {
			continue
		}
		g, seen := gone[dir]
		if !seen {
			g = hookDirGone(dir)
			gone[dir] = g
		}
		if !g {
			continue
		}
		name := ptr.Value(ls.DisplayName)
		if name == "" {
			name = ls.AgentName
		}
		out = append(out, hooksAgent{SessionID: ls.SessionID, Name: name, Team: ptr.Value(ls.BoardName), Status: "missing"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RefreshHooks resumes one Claude agent on the current hook set: Sleep then Wake (the same
// session and conversation). An agent in a turn or waiting on a prompt is not interrupted unless
// the Operator confirmed ({"force": true}): 409 "busy". One refresh per session at a time: a
// second request while one runs gets 409 "refreshing" (#3685).
// POST /api/sessions/live/{sessionID}/refresh-hooks (Operator only; Launch through Coral Hub)
//
// By decision (#3665 review, #3685), a Coral Hub user at Launch tier may force it too: Launch
// already lets them restart, sleep and wake an agent, which cut a turn the same way.
func (h *SessionsHandler) RefreshHooks(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS()
	sessionID := chi.URLParam(r, "sessionID")
	var body struct {
		Force bool `json:"force"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			errBadRequest(w, "invalid JSON")
			return
		}
	}
	if _, running := h.hooksRefreshing.LoadOrStore(sessionID, struct{}{}); running {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "code": "refreshing",
			"error": "this agent is already being resumed on the current hooks"})
		return
	}
	defer h.hooksRefreshing.Delete(sessionID)
	if h.onRefreshHeld != nil {
		h.onRefreshHeld(sessionID)
	}
	ctx := r.Context()
	ls, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil || ls == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Session not found"})
		return
	}
	if ls.AgentType != at.Claude && !h.hooksUnreachable(ctx, sessionID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "only Claude agents have a hook set to update"})
		return
	}
	if ls.IsSleeping == 1 {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "code": "sleeping", "error": "the agent is asleep: waking it loads the current hooks"})
		return
	}
	if st := h.deriveSingleSessionState(ctx, sessionID, 0); (st.InTurn || st.NeedsInput) && !body.Force {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "code": "busy",
			"error": "the agent is in a turn or waiting on a prompt: resume it when it is idle, or confirm to stop its turn"})
		return
	}
	// Sleep, with Coral's reason: a crash before the wake leaves it for the next start's
	// auto-wake, like an agent Coral stopped.
	if err := h.ss.SetSessionSleepingFor(ctx, sessionID, store.SleepReasonHooksUpdate); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if h.terminal != nil {
		if err := h.terminal.KillSessionOnly(ctx, ls.AgentName, ls.AgentType, sessionID); err != nil {
			slog.Warn("hooks update: ending the agent's terminal", "session_id", sessionID, "error", err)
		}
	}
	slept, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil || slept == nil {
		errInternalServer(w, "the agent's record went away")
		return
	}
	if err := h.wakeExistingSession(ctx, slept); err != nil {
		slog.Warn("hooks update: waking the agent", "session_id", sessionID, "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "the agent was stopped but could not be started again: wake it from the list"})
		return
	}
	h.sendAutoWakeNotice(ctx, *slept) // its board hears that its last turn may not have finished
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hooks_rev": agent.ClaudeHooksRev})
}
