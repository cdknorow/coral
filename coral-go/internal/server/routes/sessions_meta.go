package routes

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/safego"
)

// SetDisplayName sets the display name for a live session.
// PUT /api/sessions/live/{name}/display-name
func (h *SessionsHandler) SetDisplayName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName string `json:"display_name"`
		SessionID   string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.SessionID == "" || body.DisplayName == "" {
		errBadRequest(w, "session_id and display_name required")
		return
	}

	if err := h.ss.SetDisplayName(r.Context(), body.SessionID, body.DisplayName); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "display_name": body.DisplayName})
}

// SetIcon sets or clears the emoji icon for a live session.
// PUT /api/sessions/live/{name}/icon
func (h *SessionsHandler) SetIcon(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		Icon      string `json:"icon"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.SessionID == "" {
		errBadRequest(w, "session_id is required")
		return
	}

	var icon *string
	trimmed := strings.TrimSpace(body.Icon)
	if trimmed != "" {
		icon = &trimmed
	}

	if err := h.ss.SetIcon(r.Context(), body.SessionID, icon); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "icon": icon})
}

// SetNameColor sets or clears the sidebar name colour for a live session.
// PUT /api/sessions/live/{name}/name-color  {"session_id": "...", "color": "#rrggbb" | ""}
func (h *SessionsHandler) SetNameColor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		Color     string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.SessionID == "" {
		errBadRequest(w, "session_id is required")
		return
	}

	var color *string
	trimmed := strings.ToLower(strings.TrimSpace(body.Color))
	if trimmed != "" {
		// The value is rendered into a style attribute, so accept only #rrggbb.
		if !nameColorRe.MatchString(trimmed) {
			errBadRequest(w, "color must be #rrggbb")
			return
		}
		color = &trimmed
	}

	if err := h.ss.SetNameColor(r.Context(), body.SessionID, color); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name_color": color})
}

var nameColorRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// POST /api/sessions/live/{name}/context-window
// Updates the context window (and optionally model) for a live session.
// Called by the coral-hook-session-start hook when a SessionStart event fires.
func (h *SessionsHandler) UpdateContextWindow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		Model     string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// Only the agent's own SessionStart hook reports its model (#3495).
	sid, ok := callerSession(w, r, body.SessionID, false, nil)
	if !ok {
		return
	}
	body.SessionID = sid
	if body.SessionID == "" || body.Model == "" {
		errBadRequest(w, "session_id and model are required")
		return
	}

	contextWindow := proxy.LookupContextWindow(body.Model)
	if contextWindow == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "context_window": 0, "skipped": true})
		return
	}

	if err := h.ss.UpdateContextWindow(r.Context(), body.SessionID, contextWindow, body.Model); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "context_window": contextWindow, "model": body.Model})
}

// recordCLIVersion records which CLI version a launched agent runs (#3481). It asks the
// CLI (--version, cached per binary), so it runs after the launch, not before.
func (h *SessionsHandler) recordCLIVersion(sessionID, agentType string, settings map[string]string) {
	versionOf := agent.CLIVersion
	if h.cliVersionOf != nil {
		versionOf = h.cliVersionOf
	}
	if v := versionOf(settings, agentType); v != "" {
		if err := h.ss.SetCLIVersion(context.Background(), sessionID, v); err != nil {
			slog.Warn("could not record the agent's CLI version", "session_id", sessionID, "error", err)
		}
	}
}

// BackfillCLIVersions records the CLI version of each live session that has none (#3573). The
// version is recorded at launch, so an agent launched before #3481, or by a build without it,
// kept an empty one across restarts, and every decision line said cli_version="". It costs one
// --version call per CLI binary (cached). The server runs it once, in the background, at start.
func (h *SessionsHandler) BackfillCLIVersions(ctx context.Context) {
	live, err := h.ss.GetAllLiveSessions(ctx)
	if err != nil {
		return
	}
	have, _ := h.ss.CLIVersions(ctx)
	settings, _ := h.ss.GetSettings(ctx)
	for _, s := range live {
		if have[s.SessionID] != "" || s.AgentType == "" {
			continue
		}
		if _, tried := h.cliVersionTried.LoadOrStore(s.SessionID, true); tried {
			continue
		}
		h.recordCLIVersion(s.SessionID, s.AgentType, settings)
	}
}

// ensureCLIVersion looks up a session's missing CLI version in the background, once per session
// (one that started after the backfill, or whose CLI couldn't say), so a decision is never held
// up by a --version call.
func (h *SessionsHandler) ensureCLIVersion(sessionID, agentType string) {
	if sessionID == "" || agentType == "" {
		return
	}
	if _, tried := h.cliVersionTried.LoadOrStore(sessionID, true); tried {
		return
	}
	go func() {
		defer safego.Recover("server/routes/sessions_meta:185")
		settings, _ := h.ss.GetSettings(context.Background())
		h.recordCLIVersion(sessionID, agentType, settings)
	}()
}

// runningCLIVersion is the CLI version a session's agent runs, for each decision line (#3573).
// For Claude it is the newest transcript line's version: Claude Code updates itself while an
// agent keeps running, so this is re-read every time (only the transcript's new lines are read)
// and the recorded version follows it. Otherwise, or without a transcript yet, it is the
// recorded version, asked for once in the background when there is none.
func (h *SessionsHandler) runningCLIVersion(ctx context.Context, agentType, sessionID, workingDir string) string {
	recorded := h.ss.CLIVersion(ctx, sessionID)
	if agentType == "claude" && h.jsonl != nil {
		if v := h.jsonl.ReadClaudeVersion(sessionID, workingDir); v != "" {
			if v != recorded {
				if err := h.ss.SetCLIVersion(ctx, sessionID, v); err != nil {
					slog.Warn("could not record the agent's CLI version", "session_id", sessionID, "error", err)
				}
			}
			return v
		}
	}
	if recorded == "" {
		h.ensureCLIVersion(sessionID, agentType) // the next decision line has it
	}
	return recorded
}
