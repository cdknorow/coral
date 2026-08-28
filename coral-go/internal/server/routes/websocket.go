package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
)

// wsAcceptOptions returns WebSocket accept options with origin validation.
// The nhooyr.io/websocket library automatically allows same-origin requests
// (where Origin host matches the request Host). OriginPatterns adds
// cross-origin exceptions for localhost variants and the actual request host
// so that remote access (where the browser's Origin may differ from the
// bound address) works correctly.
func (h *SessionsHandler) wsAcceptOptions(r *http.Request) *websocket.AcceptOptions {
	patterns := []string{
		"localhost",
		"localhost:*",
		"127.0.0.1",
		"127.0.0.1:*",
		"[::1]",
		"[::1]:*",
	}
	// When bound to 0.0.0.0, the request Host (e.g. 192.168.1.5:8450)
	// won't match the library's same-origin check. Add the request host
	// as an allowed origin so remote access works.
	if host := r.Host; host != "" {
		patterns = append(patterns, host)
	}
	return &websocket.AcceptOptions{
		OriginPatterns: patterns,
	}
}

// ── /ws/coral — Diff-based session list streaming ────────────────────

// WSCoral streams the coral-wide session list via WebSocket.
//
// First message is a full "coral_update" with all sessions.
// Subsequent messages are "coral_diff" with only changed/removed sessions
// to reduce bandwidth. Full session objects are sent per changed agent
// (no field-level diffs).
func (h *SessionsHandler) WSCoral(w http.ResponseWriter, r *http.Request) {
	if debugEnabled() {
		slog.Info("[debug] ws/coral connection from", "remote", r.RemoteAddr, "origin", r.Header.Get("Origin"))
	}
	conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r))
	if err != nil {
		slog.Debug("ws/coral accept failed", "error", err)
		return
	}
	defer func() {
		if debugEnabled() {
			slog.Info("[debug] ws/coral disconnected", "remote", r.RemoteAddr)
		}
		conn.CloseNow()
	}()

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

		// Drain pending notifications
		var notifications []Notification
		if h.notifications != nil {
			notifications = h.notifications.Drain()
		}

		if firstMessage {
			msg := map[string]any{
				"type":        "coral_update",
				"sessions":    sessions,
				"active_runs": activeRuns,
			}
			if len(notifications) > 0 {
				msg["notifications"] = notifications
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

		hasNotifications := len(notifications) > 0
		if len(changed) > 0 || len(removed) > 0 || runsChanged || hasNotifications {
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
			if hasNotifications {
				payload["notifications"] = notifications
			}
			if err := wsjson.Write(ctx, conn, payload); err != nil {
				return
			}
			prevSessions = currSessions
			prevRunsJSON = currRunsStr
		}
	}
}

// buildSessionListForWS builds the enriched session list (same fields as List handler).
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
	icons, _ := h.ss.GetIcons(ctx, sessionIDs)
	if icons == nil {
		icons = map[string]string{}
	}

	// Latest events for waiting/done/working state
	latestEvents, _ := h.ts.GetLatestEventTypes(ctx, sessionIDs)
	if latestEvents == nil {
		latestEvents = map[string][2]string{}
	}

	// Board subscriptions (keyed by tmux session name)
	var boardSubs map[string]*board.Subscriber
	if h.bs != nil {
		boardSubs, _ = h.bs.GetAllSubscriptions(ctx)
	}
	if boardSubs == nil {
		boardSubs = map[string]*board.Subscriber{}
	}

	// Fallback: board_name from live_sessions DB
	liveBoardNames := make(map[string][2]string)
	{
		var rows []struct {
			SessionID   string  `db:"session_id"`
			BoardName   *string `db:"board_name"`
			DisplayName *string `db:"display_name"`
		}
		if err := h.db.SelectContext(ctx, &rows, "SELECT session_id, board_name, display_name FROM live_sessions WHERE board_name IS NOT NULL AND status = 'active'"); err == nil {
			for _, r := range rows {
				bn, dn := "", ""
				if r.BoardName != nil { bn = *r.BoardName }
				if r.DisplayName != nil { dn = *r.DisplayName }
				liveBoardNames[r.SessionID] = [2]string{bn, dn}
			}
		}
	}

	fileCounts, _ := h.ts.GetAllEditedFileCounts(ctx)
	if fileCounts == nil {
		fileCounts = map[string]int{}
	}

	// Board unread counts
	var allUnread map[string]int
	if h.bs != nil {
		allUnread, _ = h.bs.GetAllUnreadCounts(ctx)
	}
	if allUnread == nil {
		allUnread = map[string]int{}
	}

	// Latest goals for summary fallback
	latestGoals, _ := h.ts.GetLatestGoals(ctx, sessionIDs)
	if latestGoals == nil {
		latestGoals = map[string]string{}
	}

	// Token usage
	var tokenUsageMap map[string]*store.TokenUsage
	var latestTurnCtx map[string]int
	if h.tokenStore != nil && len(sessionIDs) > 0 {
		tokenUsageMap, _ = h.tokenStore.GetLatestUsageBySessionIDs(ctx, sessionIDs)
		var ctxErr error
		latestTurnCtx, ctxErr = h.tokenStore.GetLatestTurnContextBySessionIDs(ctx, sessionIDs)
		if ctxErr != nil {
			slog.Warn("GetLatestTurnContextBySessionIDs failed", "error", ctxErr, "num_sessions", len(sessionIDs))
		}
	}
	if tokenUsageMap == nil {
		tokenUsageMap = map[string]*store.TokenUsage{}
	}
	if latestTurnCtx == nil {
		latestTurnCtx = map[string]int{}
	}

	// Context window from live sessions
	allLive, _ := h.ss.GetAllLiveSessions(ctx)
	ctxWindowMap := make(map[string]int, len(allLive))
	createdAtMap := make(map[string]string, len(allLive))
	for _, ls := range allLive {
		if ls.ContextWindow > 0 {
			ctxWindowMap[ls.SessionID] = ls.ContextWindow
		}
		createdAtMap[ls.SessionID] = ls.CreatedAt
	}

	var sessions []map[string]any
	liveSIDs := make(map[string]bool)
	for _, agent := range agents {
		logInfo := getLogStatus(agent.LogPath)
		sid := agent.SessionID

		// Compute waiting/done/working from latest event
		ev := latestEvents[sid]
		latestEv := ev[0]
		evSummary := ev[1]
		needsInput := latestEv == "notification"
		done := latestEv == "stop"
		staleF, _ := logInfo["staleness_seconds"].(float64)
		working := (latestEv == "tool_use" || latestEv == "prompt_submit") && staleF < 120
		if working && strings.HasPrefix(evSummary, "Ran: sleep") {
			working = false
		}
		notStarted := agentNeverStarted(agent.AgentType, latestEv, staleF, sessionAgeSeconds(createdAtMap[sid]))

		var waitingReason, waitingSummary any
		if needsInput {
			waitingReason = latestEv
			waitingSummary = evSummary
		}

		// Summary fallback to latest goal
		summary, _ := logInfo["summary"].(string)
		if summary == "" {
			if goal, ok := latestGoals[sid]; ok {
				summary = goal
			}
		}

		// Board unread
		tmuxName := agent.TmuxSession
		boardUnread := 0
		if boardSubs[tmuxName] != nil {
			boardUnread = allUnread[tmuxName]
		}

		entry := map[string]any{
			"name":               agent.AgentName,
			"agent_type":         agent.AgentType,
			"session_id":         sid,
			"tmux_session":       agent.TmuxSession,
			"status":             logInfo["status"],
			"summary":            summary,
			"staleness_seconds":  logInfo["staleness_seconds"],
			"display_name":       nilIfEmpty(displayNames[sid]),
			"icon":               nilIfEmpty(icons[sid]),
			"working_directory":  agent.WorkingDir,
			"waiting_for_input":  needsInput,
			"not_started":        notStarted,
			"done":               done,
			"stuck":              false,
			"waiting_reason":     waitingReason,
			"waiting_summary":    waitingSummary,
			"working":            working,
			"changed_file_count": fileCounts[sid],
			"commands":           map[string]string{"compress": "/compact", "clear": "/clear"},
			"board_project":      boardProject(boardSubs, liveBoardNames, agent.TmuxSession, sid),
			"board_job_title":    boardJobTitle(boardSubs, liveBoardNames, agent.TmuxSession, sid),
			"board_unread":       boardUnread,
			"log_path":           agent.LogPath,
			"sleeping":           false,
		}
		if usage, ok := tokenUsageMap[sid]; ok {
			entry["token_input"] = usage.InputTokens
			entry["token_output"] = usage.OutputTokens
			entry["token_cost_usd"] = usage.CostUSD
		}
		if cw, ok := ctxWindowMap[sid]; ok && cw > 0 {
			entry["context_window"] = cw
			if turnCtx, ok := latestTurnCtx[sid]; ok && turnCtx > 0 {
				pct := int(float64(turnCtx) / float64(cw) * 100)
				if pct > 100 {
					pct = 100
				}
				entry["context_pct"] = pct
			}
		}
		liveSIDs[sid] = true
		sessions = append(sessions, entry)
	}

	// Add placeholder entries for sleeping sessions without active tmux
	for _, ls := range allLive {
		if ls.IsSleeping != 1 || liveSIDs[ls.SessionID] {
			continue
		}
		bp, dn := "", ""
		if ls.BoardName != nil {
			bp = *ls.BoardName
		}
		if ls.DisplayName != nil {
			dn = *ls.DisplayName
		}
		sessions = append(sessions, map[string]any{
			"name":               ls.AgentName,
			"agent_type":         ls.AgentType,
			"session_id":         ls.SessionID,
			"tmux_session":       nil,
			"status":             "Sleeping",
			"summary":            nil,
			"staleness_seconds":  nil,
			"working_directory":  ls.WorkingDir,
			"display_name":       dn,
			"icon":               ls.Icon,
			"branch":             nil,
			"waiting_for_input":  false,
			"not_started":        false,
			"done":               false,
			"waiting_reason":     nil,
			"waiting_summary":    nil,
			"working":            false,
			"stuck":              false,
			"changed_file_count": 0,
			"commands":           map[string]string{"compress": "/compact", "clear": "/clear"},
			"board_project":      bp,
			"board_job_title":    dn,
			"board_unread":       0,
			"log_path":           "",
			"sleeping":           true,
		})
	}

	return sessions, nil
}

// getActiveRuns fetches active job runs for the Jobs sidebar.
func (h *SessionsHandler) getActiveRuns(ctx context.Context) []map[string]any {
	if h.schedStore == nil {
		return []map[string]any{}
	}
	runs, err := h.schedStore.ListActiveRuns(ctx)
	if err != nil {
		return []map[string]any{}
	}
	result := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		entry := map[string]any{
			"id":           r.ID,
			"job_id":       r.JobID,
			"status":       r.Status,
			"scheduled_at": r.ScheduledAt,
			"created_at":   r.CreatedAt,
		}
		if r.JobName != nil {
			entry["job_name"] = *r.JobName
		}
		if r.SessionID != nil {
			entry["session_id"] = *r.SessionID
		}
		if r.StartedAt != nil {
			entry["started_at"] = *r.StartedAt
		}
		if r.DisplayName != nil {
			entry["display_name"] = *r.DisplayName
		}
		result = append(result, entry)
	}
	return result
}

// ── /ws/terminal/{name} — Bidirectional terminal WebSocket ──────────

// WSTerminal provides bidirectional terminal WebSocket with unified streaming.
// Both PTY and tmux backends use the same code path: Attach for live output,
// Replay for initial seed. Stream data is sent as binary WebSocket frames;
// control messages (terminal_closed) remain JSON text frames.
func (h *SessionsHandler) WSTerminal(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	if debugEnabled() {
		slog.Info("[debug] ws/terminal connect", "name", name, "remote", r.RemoteAddr)
	}

	conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r))
	if err != nil {
		slog.Debug("ws/terminal accept failed", "error", err)
		return
	}
	defer func() {
		if debugEnabled() {
			slog.Info("[debug] ws/terminal disconnected", "name", name)
		}
		conn.CloseNow()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if h.backend == nil {
		conn.Close(websocket.StatusInternalError, "no backend")
		return
	}

	subID := fmt.Sprintf("ws-%d", time.Now().UnixNano())
	ch, err := h.backend.Attach(name, subID)
	if err != nil {
		if debugEnabled() {
			slog.Info("[debug] ws/terminal attach failed", "name", name, "error", err)
		}
		conn.Close(websocket.StatusInternalError, "attach failed")
		return
	}

	defer h.backend.Unsubscribe(name, subID)

	// Send replay seed as a binary frame, prefixed with clear codes so
	// reconnects don't stack duplicate scrollback.
	if replay, err := h.backend.Replay(name); err == nil && len(replay) > 0 {
		clearPrefix := []byte("\x1b[2J\x1b[3J\x1b[H")
		seed := make([]byte, len(clearPrefix)+len(replay))
		copy(seed, clearPrefix)
		copy(seed[len(clearPrefix):], replay)
		conn.Write(ctx, websocket.MessageBinary, seed)
	}

	// Reader goroutine: receives terminal input from the client (JSON text frames)
	go func() {
		defer cancel()
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var msg struct {
				Type string `json:"type"`
				Data string `json:"data"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "terminal_input":
				if msg.Data != "" {
					h.backend.SendInput(name, []byte(msg.Data))
				}
			case "terminal_resize":
				if msg.Cols >= 10 {
					rows := uint16(msg.Rows)
					if rows == 0 {
						rows = 50
					}
					h.backend.Resize(name, uint16(msg.Cols), rows)
				}
			}
		}
	}()

	// Writer loop: forward live output as binary WebSocket frames
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case data, ok := <-ch:
			if !ok {
				wsjson.Write(ctx, conn, map[string]any{"type": "terminal_closed"})
				conn.Close(websocket.StatusNormalClosure, "session ended")
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
				return
			}
		}
	}
}

const (
	// notStartedThresholdSeconds is how long an agent may produce nothing before
	// we tell the user to look at its terminal. Long enough that a slow start is
	// not mistaken for a stall; short enough that a new user has not given up.
	notStartedThresholdSeconds = 25

	// notStartedWindowSeconds bounds how long after launch this is reported, so
	// a long-lived agent that is simply unused does not carry the label forever.
	notStartedWindowSeconds = 15 * 60
)

// agentNeverStarted reports that an agent has written terminal output, has
// never emitted a single agent event, and has been silent since — within a
// window after launch.
//
// What it does NOT claim is that the agent is blocked. Measured against a real
// agent: it is true while the agent sits on Claude Code's trust-folder prompt,
// and equally true once that prompt is answered and the agent waits idle at its
// own REPL having been given nothing to do. From outside the CLI those two are
// indistinguishable — both are a live process waiting on stdin — and pretending
// otherwise would mean matching one CLI's prompt wording, which breaks the
// first time that wording changes and covers only the CLI we encoded.
//
// So this reports the union, which is a true statement about both: this agent
// has not done anything yet, and its terminal is where the answer is. It stops
// the moment the agent emits its first event, and after the window regardless.
//
// Terminal sessions are excluded: they are a shell, they never emit agent
// events, and sitting at a prompt is what they are for.
func agentNeverStarted(agentType, latestEvent string, stalenessSeconds, sessionAgeSeconds float64) bool {
	if agentType == at.Terminal {
		return false
	}
	if latestEvent != "" {
		return false // it started; whatever it is doing now, it is not this
	}
	if sessionAgeSeconds <= 0 || sessionAgeSeconds > notStartedWindowSeconds {
		return false
	}
	return stalenessSeconds >= notStartedThresholdSeconds
}

// sessionAgeSeconds returns how long ago a session was created, or 0 when the
// timestamp is missing or unparseable.
func sessionAgeSeconds(createdAt string) float64 {
	if createdAt == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return 0
	}
	age := time.Since(t).Seconds()
	if age < 0 {
		return 0
	}
	return age
}
