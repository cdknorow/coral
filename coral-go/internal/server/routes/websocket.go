package routes

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// ── /ws/coral — Diff-based session list streaming ────────────────────

// WSCoral streams the coral-wide session list via WebSocket.
//
// First message is a full "coral_update" with all sessions.
// Subsequent messages are "coral_diff" with only changed/removed sessions
// to reduce bandwidth. Full session objects are sent per changed agent
// (no field-level diffs).
func (h *SessionsHandler) WSCoral(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Allow localhost origins (matches CORS config)
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Debug("ws/coral accept failed", "error", err)
		return
	}
	defer conn.CloseNow()

	ctx := conn.CloseRead(r.Context())

	// Per-connection state for diff calculation
	prevSessions := make(map[string]string) // session key -> json string
	prevRunsJSON := "[]"
	firstMessage := true

	pollInterval := time.Duration(h.cfg.WSPollIntervalS) * time.Second
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case <-ticker.C:
		}

		sessions, err := h.buildSessionListForWS(r)
		if err != nil {
			slog.Warn("ws/coral build session list failed", "error", err)
			continue
		}

		// Fetch active job runs
		activeRuns := h.getActiveRuns(r.Context())

		// Build per-session state map for diff
		currSessions := make(map[string]string, len(sessions))
		sessionByKey := make(map[string]map[string]any, len(sessions))
		for _, s := range sessions {
			key := ""
			if sid, ok := s["session_id"].(string); ok && sid != "" {
				key = sid
			} else if name, ok := s["name"].(string); ok {
				key = name
			}
			if key == "" {
				continue
			}
			serialized, _ := json.Marshal(s)
			currSessions[key] = string(serialized)
			sessionByKey[key] = s
		}

		currRunsJSON, _ := json.Marshal(activeRuns)
		currRunsStr := string(currRunsJSON)

		if firstMessage {
			msg := map[string]any{
				"type":        "coral_update",
				"sessions":    sessions,
				"active_runs": activeRuns,
			}
			if err := wsjson.Write(ctx, conn, msg); err != nil {
				return
			}
			prevSessions = currSessions
			prevRunsJSON = currRunsStr
			firstMessage = false
			continue
		}

		// Calculate diff
		var changed []map[string]any
		for key, sJSON := range currSessions {
			if prevSessions[key] != sJSON {
				changed = append(changed, sessionByKey[key])
			}
		}

		var removed []string
		for key := range prevSessions {
			if _, exists := currSessions[key]; !exists {
				removed = append(removed, key)
			}
		}

		runsChanged := currRunsStr != prevRunsJSON

		if len(changed) > 0 || len(removed) > 0 || runsChanged {
			payload := map[string]any{"type": "coral_diff"}
			if len(changed) > 0 {
				payload["changed"] = changed
			}
			if len(removed) > 0 {
				payload["removed"] = removed
			}
			if runsChanged {
				payload["active_runs"] = activeRuns
			}
			if err := wsjson.Write(ctx, conn, payload); err != nil {
				return
			}
			prevSessions = currSessions
			prevRunsJSON = currRunsStr
		}
	}
}

// buildSessionListForWS builds the enriched session list (same as List handler).
func (h *SessionsHandler) buildSessionListForWS(r *http.Request) ([]map[string]any, error) {
	agents, err := h.discoverAgents(r)
	if err != nil {
		return nil, err
	}

	ctx := r.Context()
	sessionIDs := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.SessionID != "" {
			sessionIDs = append(sessionIDs, a.SessionID)
		}
	}
	displayNames, _ := h.ss.GetDisplayNames(ctx, sessionIDs)

	var sessions []map[string]any
	for _, agent := range agents {
		logInfo := getLogStatus(agent.LogPath)
		entry := map[string]any{
			"name":                agent.AgentName,
			"agent_type":         agent.AgentType,
			"session_id":         agent.SessionID,
			"tmux_session":       agent.TmuxSession,
			"status":             logInfo["status"],
			"summary":            logInfo["summary"],
			"staleness_seconds":  logInfo["staleness_seconds"],
			"display_name":       nilIfEmpty(displayNames[agent.SessionID]),
			"working_directory":  agent.WorkingDir,
			"changed_file_count": 0,
			"log_path":           agent.LogPath,
		}
		sessions = append(sessions, entry)
	}
	return sessions, nil
}

// getActiveRuns fetches active job runs for the Jobs sidebar.
func (h *SessionsHandler) getActiveRuns(ctx context.Context) []map[string]any {
	// Stub — will be wired when ScheduleStore is injected into SessionsHandler
	return []map[string]any{}
}

// ── /ws/terminal/{name} — Bidirectional terminal WebSocket ──────────

// WSTerminal provides bidirectional terminal WebSocket.
//
// Server → Client: polls tmux pane content and pushes "terminal_update" messages.
// Client → Server: receives "terminal_input" messages and forwards data to tmux.
// Uses adaptive polling: 50ms after recent input, 300ms when idle.
func (h *SessionsHandler) WSTerminal(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Debug("ws/terminal accept failed", "error", err)
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Resolve pane target once to avoid repeated tmux list-panes lookups
	target, err := h.tmux.FindPaneTarget(ctx, name, agentType, sessionID)
	if err != nil || target == "" {
		conn.Close(websocket.StatusInternalError, "pane not found")
		return
	}

	var (
		lastContent string
		closed      bool
		closedMu    sync.Mutex
		inputEvent  = make(chan struct{}, 1)
		targetMu    sync.Mutex
	)

	isClosed := func() bool {
		closedMu.Lock()
		defer closedMu.Unlock()
		return closed
	}
	setClosed := func() {
		closedMu.Lock()
		closed = true
		closedMu.Unlock()
		cancel()
	}

	// Reader goroutine: receives terminal input from the client
	go func() {
		defer setClosed()
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}

			var msg struct {
				Type string `json:"type"`
				Data string `json:"data"`
				Cols int    `json:"cols"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}

			targetMu.Lock()
			currentTarget := target
			targetMu.Unlock()

			switch msg.Type {
			case "terminal_input":
				if msg.Data != "" && currentTarget != "" {
					h.tmux.SendTerminalInputToTarget(ctx, currentTarget, msg.Data)
					// Wake the writer for fast echo
					select {
					case inputEvent <- struct{}{}:
					default:
					}
				}
			case "terminal_resize":
				if msg.Cols >= 10 && currentTarget != "" {
					h.tmux.ResizePaneTarget(ctx, currentTarget, msg.Cols)
				}
			}
		}
	}()

	// Writer loop: polls tmux pane and pushes content to client
	const (
		idleInterval   = 300 * time.Millisecond
		activeInterval = 50 * time.Millisecond
	)
	interval := idleInterval

	for !isClosed() {
		// Re-resolve target if initially unavailable
		targetMu.Lock()
		currentTarget := target
		targetMu.Unlock()
		if currentTarget == "" {
			newTarget, _ := h.tmux.FindPaneTarget(ctx, name, agentType, sessionID)
			if newTarget != "" {
				targetMu.Lock()
				target = newTarget
				currentTarget = newTarget
				targetMu.Unlock()
			}
		}

		if currentTarget != "" {
			content, _ := h.tmux.CapturePaneRawTarget(ctx, currentTarget, 200)
			if content != "" && content != lastContent {
				msg := map[string]any{
					"type":    "terminal_update",
					"content": content,
				}
				if err := wsjson.Write(ctx, conn, msg); err != nil {
					return
				}
				lastContent = content
			}
		}

		// Wait for either the interval or an input event
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case <-inputEvent:
			// Input happened — use fast poll
			interval = activeInterval
		case <-time.After(interval):
			// No input — drift back toward idle rate
			if interval < idleInterval {
				interval += 50 * time.Millisecond
				if interval > idleInterval {
					interval = idleInterval
				}
			}
		}
	}

	conn.Close(websocket.StatusNormalClosure, "")
}
