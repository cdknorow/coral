package routes

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
	"unicode/utf8"
)

// A tool call the agent has started but not finished, reported by the
// PreToolUse hook. Claude Code writes a tool call to the transcript only once
// it completes, so while a permission prompt, an AskUserQuestion or a plan
// approval is open this is the only place the chat can learn what is being
// asked. Kept in memory: it only matters while the prompt is on screen.
type pendingTool struct {
	ToolUseID string         `json:"tool_use_id"`
	ToolName  string         `json:"tool_name"`
	Input     map[string]any `json:"input"`
	At        time.Time      `json:"at"`
}

type pendingTools struct {
	mu sync.Mutex
	m  map[string]pendingTool // session_id -> latest started tool
	// calls are all the tool calls a session has started and not finished (tool_use_id -> the call), so a permission request can be tied to the calls it may be for (#6687): the slot above
	// holds only the newest, and agents run tool calls in parallel.
	calls map[string]map[string]pendingTool
}

// maxCallsPerSession bounds what is remembered of one session's calls in flight.
const maxCallsPerSession = 64

// A prompt left open longer than this is stale (e.g. the hook for its
// completion never arrived).
const pendingToolTTL = 30 * time.Minute

func (p *pendingTools) set(sessionID string, t pendingTool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]pendingTool{}
	}
	p.m[sessionID] = t
	if t.ToolUseID != "" && len(t.ToolUseID) <= 128 {
		if p.calls == nil {
			p.calls = map[string]map[string]pendingTool{}
		}
		cs := p.calls[sessionID]
		if cs == nil {
			cs = map[string]pendingTool{}
			p.calls[sessionID] = cs
		}
		if len(cs) >= maxCallsPerSession { // a session that never reports its calls finishing: the oldest go
			oldest, at := "", time.Time{}
			for id, c := range cs {
				if oldest == "" || c.At.Before(at) {
					oldest, at = id, c.At
				}
			}
			delete(cs, oldest)
		}
		cs[t.ToolUseID] = pendingTool{ToolUseID: t.ToolUseID, ToolName: t.ToolName, At: t.At}
	}
}

// callDone drops a finished call; with no id, all of the session's calls (a turn's end, a new turn).
func (p *pendingTools) callDone(sessionID, toolUseID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if toolUseID == "" {
		delete(p.calls, sessionID)
		return
	}
	delete(p.calls[sessionID], toolUseID)
}

// callsFor are the ids of the session's calls of tool still in flight that started within maxAge: the calls a permission request for tool may be about.
func (p *pendingTools) callsFor(sessionID, tool string, now time.Time, maxAge time.Duration) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []string
	for id, c := range p.calls[sessionID] {
		if c.ToolName == tool && !c.At.IsZero() && now.Sub(c.At) <= maxAge {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (p *pendingTools) get(sessionID string, now time.Time) (pendingTool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.m[sessionID]
	if ok && now.Sub(t.At) > pendingToolTTL {
		delete(p.m, sessionID)
		return pendingTool{}, false
	}
	return t, ok
}

// prune drops the pending tool of every session isLive refuses, and any older than pendingToolTTL, and returns how many it
// dropped (#4602, L6: a session that ended with a prompt open left its entry for ever).
func (p *pendingTools) prune(isLive func(sessionID string) bool, now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for id, t := range p.m {
		if !isLive(id) || now.Sub(t.At) > pendingToolTTL {
			delete(p.m, id)
			n++
		}
	}
	return n
}

// durationMs returns the server-observed duration of a matching tool call.
// Some agents report duration_ms themselves; Codex currently does not, so its
// PreToolUse and PostToolUse hook arrival times provide the fallback.
func (p *pendingTools) durationMs(sessionID, toolUseID string, now time.Time) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.m[sessionID]
	if !ok || t.At.IsZero() || now.Before(t.At) || now.Sub(t.At) > pendingToolTTL {
		return 0, false
	}
	if toolUseID != "" && t.ToolUseID != "" && toolUseID != t.ToolUseID {
		return 0, false
	}
	ms := now.Sub(t.At).Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return ms, true
}

// clear drops the pending tool for a session. With a tool_use_id, only that
// call is cleared (a newer call that started meanwhile stays).
func (p *pendingTools) clear(sessionID, toolUseID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t, ok := p.m[sessionID]; ok && (toolUseID == "" || t.ToolUseID == toolUseID) {
		delete(p.m, sessionID)
	}
}

// Only the fields the chat shows are kept, each capped, so a large Write or
// Edit payload is never held or served.
var pendingToolFields = map[string]int{
	"questions":    20000, // AskUserQuestion (serialized size cap)
	"plan":         20000, // ExitPlanMode
	"command":      4000,
	"CommandLine":  4000,
	"description":  500,
	"file_path":    1000,
	"TargetFile":   1000,
	"AbsolutePath": 1000,
	"path":         1000,
	"url":          2000,
	"query":        500,
	"pattern":      500,
	"prompt":       2000,
}

func trimPendingInput(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, limit := range pendingToolFields {
		v, ok := in[k]
		if !ok || v == nil {
			continue
		}
		switch s := v.(type) {
		case string:
			out[k] = truncateUTF8(s, limit)
		default:
			if b, err := json.Marshal(v); err == nil && len(b) <= limit {
				out[k] = v
			}
		}
	}
	if out["command"] == nil && out["CommandLine"] != nil {
		out["command"] = out["CommandLine"]
	}
	if out["file_path"] == nil {
		if out["TargetFile"] != nil {
			out["file_path"] = out["TargetFile"]
		} else if out["AbsolutePath"] != nil {
			out["file_path"] = out["AbsolutePath"]
		}
	}
	return out
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// SetPendingTool records a tool call that has started (PreToolUse hook).
// POST /api/sessions/live/{name}/pending-tool
func (h *SessionsHandler) SetPendingTool(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string         `json:"session_id"`
		ToolUseID string         `json:"tool_use_id"`
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// Only the agent's own hook may say which tool it is about to run (#3481, Backend's
	// finding, like POST .../events in part 1): the pending tool tells auto-accept what an
	// approval is for, and the gate when an agy prompt can be open. The session token
	// decides the session, in every mode.
	owner, hasToken := VerifiedSession(r.Context())
	if !hasToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session_token_required",
			"message": "The pending tool comes from the agent's own hooks, with its session token"})
		return
	}
	if body.SessionID != "" && body.SessionID != owner.SessionID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "identity_mismatch",
			"message": "An agent can set the pending tool only for its own session"})
		return
	}
	body.SessionID = owner.SessionID
	if body.ToolName == "" {
		errBadRequest(w, "tool_name is required")
		return
	}
	body.ToolName = truncateUTF8(body.ToolName, 200)
	body.ToolUseID = truncateUTF8(body.ToolUseID, 200)
	h.hints.forget(body.SessionID) // a blocking tool may open a prompt: read the screen again
	t := pendingTool{
		ToolUseID: body.ToolUseID,
		ToolName:  body.ToolName,
		Input:     trimPendingInput(body.ToolInput),
		At:        time.Now(),
	}
	h.pending.set(body.SessionID, t)
	h.watchForPrompt(body.SessionID, owner.SessionName, t) // agy sends no hook for a permission prompt (#4819)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// notePendingToolEvent clears the pending tool when the event log shows it
// finished, or that the turn moved on.
func (h *SessionsHandler) notePendingToolEvent(sessionID, eventType, toolUseID string) {
	if sessionID == "" {
		return
	}
	switch eventType { // the calls in flight, whichever the single slot below holds (#6687)
	case "tool_use":
		h.pending.callDone(sessionID, toolUseID)
	case "stop", "prompt_submit", "session_reset":
		h.pending.callDone(sessionID, "")
	}
	// A PostToolUse for another tool than the pending one is a late one of an earlier call: the pending tool still runs, and whatever it waits on is
	// still open. It changes nothing (it would otherwise mark the session as waiting for nothing, and a prompt answer is refused, #5254).
	if eventType == "tool_use" && toolUseID != "" {
		if t, ok := h.pending.get(sessionID, time.Now()); ok && t.ToolUseID != "" && t.ToolUseID != toolUseID {
			return
		}
	}
	h.hints.note(sessionID, eventType)
	switch eventType {
	case "tool_use":
		if toolUseID != "" {
			h.pending.clear(sessionID, toolUseID)
		}
	case "stop", "prompt_submit", "session_reset":
		h.pending.clear(sessionID, "")
	}
}

// requestCallMaxAge is how recent a call's PreToolUse must be for a permission request to be about it.
const requestCallMaxAge = 2 * time.Minute

// maxRequestCalls bounds the calls one request waits for.
const maxRequestCalls = 8

// tieRequestToItsCall puts into a permission request's detail the ids of the calls it may be about (call_ids, #6687). The PermissionRequest hooks (Claude's, Codex's) carry no tool_use_id, but the
// calls' PreToolUse come first: the session's calls of the request's tool still in flight and under two minutes old are the candidates. One candidate is the call; several (parallel calls of the
// same tool, only one of which waits) are all of them, and the request is then answered only when EVERY one of them has finished, never by the first, so no call's PostToolUse can close it early.
// Whatever the agent put in the detail is dropped: only the server's own record names the calls. None in flight: no ids, and the request is answered by its tool's name, as before.
func (h *SessionsHandler) tieRequestToItsCall(sessionID, tool string, detail any) any {
	d, ok := detail.(map[string]any)
	if !ok {
		if detail != nil {
			return detail
		}
		d = map[string]any{}
	}
	delete(d, "tool_use_id")
	delete(d, "call_ids")
	if sessionID != "" && tool != "" {
		if ids := h.pending.callsFor(sessionID, tool, time.Now(), requestCallMaxAge); len(ids) > 0 && len(ids) <= maxRequestCalls {
			d["call_ids"] = ids
		}
	}
	if len(d) == 0 {
		return nil
	}
	return d
}
