package routes

import (
	"encoding/json"
	"net/http"
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
}

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
	"questions":   20000, // AskUserQuestion (serialized size cap)
	"plan":        20000, // ExitPlanMode
	"command":     4000,
	"description": 500,
	"file_path":   1000,
	"path":        1000,
	"url":         2000,
	"query":       500,
	"pattern":     500,
	"prompt":      2000,
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.SessionID == "" || body.ToolName == "" {
		errBadRequest(w, "session_id and tool_name are required")
		return
	}
	h.pending.set(body.SessionID, pendingTool{
		ToolUseID: body.ToolUseID,
		ToolName:  body.ToolName,
		Input:     trimPendingInput(body.ToolInput),
		At:        time.Now(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GetPendingTool returns the tool call a session has started but not
// finished, or {"pending": null}.
// GET /api/sessions/live/{name}/pending-tool?session_id=...
func (h *SessionsHandler) GetPendingTool(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if sid == "" {
		errBadRequest(w, "session_id is required")
		return
	}
	if t, ok := h.pending.get(sid, time.Now()); ok {
		writeJSON(w, http.StatusOK, map[string]any{"pending": t})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": nil})
}

// notePendingToolEvent clears the pending tool when the event log shows it
// finished, or that the turn moved on.
func (h *SessionsHandler) notePendingToolEvent(sessionID, eventType, toolUseID string) {
	if sessionID == "" {
		return
	}
	switch eventType {
	case "tool_use":
		if toolUseID != "" {
			h.pending.clear(sessionID, toolUseID)
		}
	case "stop", "prompt_submit", "session_reset":
		h.pending.clear(sessionID, "")
	}
}
