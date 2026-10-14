package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/ptymanager"
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
	var lastSent time.Time // last message written to this connection
	// A hub's feed (?feed=hub) is a machine client, not a dashboard: it must
	// not receive, and so mark delivered, this server's UI notifications.
	notifications := h.notifications
	if r.URL.Query().Get("feed") == "hub" {
		notifications = nil
	}
	// backlog: notifications no dashboard had received when this one
	// connected; they go out with the first message.
	var notifCursor int64
	var backlog []Notification
	if notifications != nil {
		backlog, notifCursor = notifications.Connect()
	}

	pollInterval := time.Duration(h.cfg.WSPollIntervalS) * time.Second
	if h.cfg.WSPollInterval > 0 {
		pollInterval = h.cfg.WSPollInterval
	}
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}
	// Remote feed changes rebuild at most once a second, or once a poll
	// interval when that is shorter (the ticker rebuilds that often anyway).
	// The limit counts only builds that carried a remote change: a kick or a
	// tick does not hold the next remote change back (#3142).
	remoteDebounce := min(time.Second, pollInterval)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Remote feed changes trigger an early rebuild (see remoteDebounce).
	var remoteChanged <-chan struct{}
	if h.remoteAgents != nil {
		ch, cancel := h.remoteAgents.Subscribe()
		defer cancel()
		remoteChanged = ch
	}
	var debounce <-chan time.Time
	// A kick (a state change on this server) rebuilds after wsKickCoalesce.
	kicked, cancelKick := h.wsKick.subscribe()
	defer cancelKick()
	var kickWait <-chan time.Time
	lastRemoteBuild := time.Time{} // last build that carried a remote change
	remotePending := false         // a remote change not yet in a build
	prevServersJSON := ""

	// The first coral_update goes out at once rather than after the first
	// tick, so a dashboard or hub feed is not empty for a poll interval. It
	// comes from the shared tick, so clients connecting together still share
	// one build.
	connected := make(chan struct{}, 1)
	connected <- struct{}{}

	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case <-connected:
		case <-ticker.C:
		case <-remoteChanged:
			if firstMessage {
				continue
			}
			remotePending = true
			if kickWait != nil {
				continue // rides the kick's build, due within wsKickCoalesce
			}
			if wait := remoteDebounce - time.Since(lastRemoteBuild); wait > 0 {
				if debounce == nil {
					debounce = time.After(wait)
				}
				continue
			}
		case <-debounce:
			debounce = nil
		case <-kicked:
			if firstMessage {
				continue // the first coral_update is built fresh anyway
			}
			if kickWait == nil {
				kickWait = time.After(wsKickCoalesce)
			}
			continue
		case <-kickWait:
			kickWait = nil
		}
		if remotePending {
			// This build carries the remote change, whatever triggered it.
			lastRemoteBuild = time.Now()
			remotePending = false
			debounce = nil
		}

		tick, err := h.sharedWSTick(r)
		if err != nil {
			slog.Warn("ws/coral build session list failed", "error", err)
			continue
		}
		// Shared with every connection: read, never modify.
		sessions := tick.sessions
		remoteServers := tick.servers
		hasRemotes := tick.hasRemotes
		activeRuns := tick.activeRuns
		currSessions := tick.current
		sessionByKey := tick.byKey
		serversJSON := tick.serversJSON
		currRunsStr := tick.runsJSON

		// Notifications this connection has not seen yet (every dashboard
		// gets each one).
		var pending []Notification
		var nextCursor int64
		if notifications != nil {
			var fresh []Notification
			fresh, nextCursor = notifications.Since(notifCursor)
			pending = append(backlog, fresh...)
		}
		// sent records a successful write: only then do these notifications
		// count as delivered and the cursor move past them.
		sent := func() {
			if notifications != nil {
				notifications.Ack(pending)
				backlog, notifCursor = nil, nextCursor
			}
		}

		if firstMessage {
			msg := map[string]any{
				"type":        "coral_update",
				"sessions":    sessions,
				"active_runs": activeRuns,
			}
			if len(pending) > 0 {
				msg["notifications"] = pending
			}
			if hasRemotes {
				msg["servers"] = remoteServers
			}
			if err := wsjson.Write(ctx, conn, msg); err != nil {
				return
			}
			sent()
			prevServersJSON = serversJSON
			prevSessions = currSessions
			prevRunsJSON = currRunsStr
			firstMessage = false
			lastSent = time.Now()
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
		var removedRemote []map[string]string
		for key := range prevSessions {
			if _, exists := currSessions[key]; !exists {
				if srv, k, ok := splitRemoteKey(key); ok {
					removedRemote = append(removedRemote, map[string]string{"server": srv, "key": k})
				} else {
					removed = append(removed, key)
				}
			}
		}
		serversChanged := serversJSON != prevServersJSON

		runsChanged := currRunsStr != prevRunsJSON

		hasNotifications := len(pending) > 0
		if len(changed) > 0 || len(removed) > 0 || len(removedRemote) > 0 || serversChanged || runsChanged || hasNotifications {
			payload := map[string]any{"type": "coral_diff"}
			if len(changed) > 0 {
				payload["changed"] = changed
			}
			if len(removed) > 0 {
				payload["removed"] = removed
			}
			if len(removedRemote) > 0 {
				payload["removed_remote"] = removedRemote
			}
			if hasRemotes && serversChanged {
				payload["servers"] = remoteServers
			}
			if runsChanged {
				payload["active_runs"] = activeRuns
			}
			if hasNotifications {
				payload["notifications"] = pending
			}
			if err := wsjson.Write(ctx, conn, payload); err != nil {
				return
			}
			sent()
			lastSent = time.Now()
			prevServersJSON = serversJSON
			prevSessions = currSessions
			prevRunsJSON = currRunsStr
		} else if time.Since(lastSent) >= wsHeartbeatInterval {
			// Nothing changed for a while. Say so, so a hub holding this feed
			// can tell an idle server from one whose tick loop is stuck (it
			// falls back to polling the list when the feed goes quiet).
			// Dashboards ignore the type.
			if err := wsjson.Write(ctx, conn, map[string]any{"type": "coral_heartbeat"}); err != nil {
				return
			}
			lastSent = time.Now()
		}
	}
}

// wsHeartbeatInterval is how long /ws/coral may stay silent before it sends a
// coral_heartbeat. It is half the hub's default FeedQuietRefresh (30s).
var wsHeartbeatInterval = 15 * time.Second

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
	nameColors, _ := h.ss.GetNameColors(ctx, sessionIDs)

	// Latest events for waiting/done/working state
	latestEvents, _ := h.ts.GetLatestEventTypes(ctx, sessionIDs)
	if latestEvents == nil {
		latestEvents = map[string][2]string{}
	}
	stateEvents, _ := h.ts.GetSessionStateEvents(ctx, sessionIDs)
	if stateEvents == nil {
		stateEvents = map[string][]store.AgentEvent{}
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
				if r.BoardName != nil {
					bn = *r.BoardName
				}
				if r.DisplayName != nil {
					dn = *r.DisplayName
				}
				liveBoardNames[r.SessionID] = [2]string{bn, dn}
			}
		}
	}

	fileCounts, _ := h.ts.GetEditedFileCounts(ctx, sessionIDs)
	if fileCounts == nil {
		fileCounts = map[string]int{}
	}

	// Board unread counts, only for the sessions being shown.
	var allUnread map[string]int
	if h.bs != nil {
		tmuxNames := make([]string, 0, len(agents))
		for _, a := range agents {
			if a.TmuxSession != "" {
				tmuxNames = append(tmuxNames, a.TmuxSession)
			}
		}
		allUnread, _ = h.bs.GetUnreadCountsForSessions(ctx, tmuxNames)
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
	capabilitiesMap := make(map[string]*string, len(allLive))
	for _, ls := range allLive {
		capabilitiesMap[ls.SessionID] = ls.Capabilities
	}

	// Git state, as /api/sessions/live sends it (branch, repo_name, git_state).
	gitStates := map[string]*store.GitSnapshot{}
	if h.gs != nil {
		sessionAgents := make(map[string]string, len(agents))
		for _, a := range agents {
			if a.SessionID != "" {
				sessionAgents[a.SessionID] = a.AgentName
			}
		}
		if states, err := h.gs.GetLiveGitState(ctx, sessionAgents); err == nil && states != nil {
			gitStates = states
		}
	}

	ctxModelMap := make(map[string]*string, len(allLive))
	createdAtMap := make(map[string]string, len(allLive))
	sleepingMap := make(map[string]bool, len(allLive))
	archivedMap := make(map[string]bool, len(allLive))
	autoAcceptStates, err := h.ss.GetAutoAcceptStates(ctx)
	if err != nil {
		slog.Warn("failed to read auto-accept states", "error", err)
	}
	for _, ls := range allLive {
		ctxModelMap[ls.SessionID] = ls.Model
		createdAtMap[ls.SessionID] = ls.CreatedAt
		sleepingMap[ls.SessionID] = ls.IsSleeping == 1
		archivedMap[ls.SessionID] = ls.IsArchived == 1
	}

	var sessions []map[string]any
	liveSIDs := make(map[string]bool)
	for _, agent := range agents {
		logInfo := h.cachedLogStatus(agent.LogPath)
		sid := agent.SessionID

		// Compute waiting/done/working from latest event
		ev := latestEvents[sid]
		latestEv := ev[0]
		staleF, _ := logInfo["staleness_seconds"].(float64)
		notStarted := agentNeverStarted(agent.AgentType, latestEv, staleF, sessionAgeSeconds(createdAtMap[sid]))
		stateInput := SessionStateInput{StalenessSeconds: staleF, NotStarted: notStarted, Sleeping: sleepingMap[sid], Archived: archivedMap[sid]}
		for _, event := range stateEvents[sid] {
			stateInput.Events = append(stateInput.Events, StateEvent{Type: event.EventType, Summary: event.Summary})
		}
		h.applyTranscriptState(&stateInput, stateEvents[sid], agent.AgentType, sid, agent.WorkingDir)
		state := DeriveSessionState(stateInput)

		var waitingReason, waitingSummary any
		if state.NeedsInput {
			waitingReason = state.WaitingReason
			waitingSummary = state.WaitingSummary
		}

		// Goal line: the newest goal event.
		summary := latestGoals[sid]
		statusVal, summaryVal, displayVal, iconVal := listTextFields(logInfo, summary, displayNames, icons, sid)

		// Board unread
		tmuxName := agent.TmuxSession
		boardUnread := 0
		if boardSubs[tmuxName] != nil {
			boardUnread = allUnread[tmuxName]
		}

		entry := map[string]any{
			"name":                  agent.AgentName,
			"agent_type":            agent.AgentType,
			"session_id":            sid,
			"tmux_session":          agent.TmuxSession,
			"status":                statusVal,
			"summary":               summaryVal,
			"staleness_seconds":     listStaleness(logInfo["staleness_seconds"]),
			"display_name":          displayVal,
			"icon":                  iconVal,
			"name_color":            nilIfEmpty(nameColors[sid]),
			"working_directory":     agent.WorkingDir,
			"waiting_for_input":     state.NeedsInput,
			"awaiting_user":         state.AwaitingUser,
			"not_started":           state.NotStarted,
			"done":                  state.Done,
			"stuck":                 state.Stuck,
			"waiting_reason":        waitingReason,
			"waiting_summary":       waitingSummary,
			"working":               state.Working,
			"changed_file_count":    fileCounts[sid],
			"commands":              map[string]string{"compress": "/compact", "clear": "/clear"},
			"board_project":         boardProject(boardSubs, liveBoardNames, agent.TmuxSession, sid),
			"board_job_title":       boardJobTitle(boardSubs, liveBoardNames, agent.TmuxSession, sid),
			"board_unread":          boardUnread,
			"board_is_orchestrator": boardSubscriberFlag(boardSubs[tmuxName]),
			"log_path":              agent.LogPath,
			"sleeping":              state.Sleeping,
			"archived":              archivedMap[sid], // set when an archived agent's pane outlived the archive
			"first_prompt":          h.jsonl.FirstUserPrompt(sid, agent.WorkingDir, agent.AgentType),
		}
		// Always present (false/""/null too): clients merge diffs, so an
		// omitted false would leave the AUTO chip on after it was turned off.
		for k, v := range autoAcceptFields(agent.AgentType, autoAcceptStates[sid]) {
			entry[k] = v
		}
		if usage, ok := tokenUsageMap[sid]; ok {
			entry["token_input"] = usage.InputTokens
			entry["token_output"] = usage.OutputTokens
			entry["token_cost_usd"] = usage.CostUSD
		}
		entry["branch"], entry["repo_name"], entry["git_state"] = gitListFields(lookupGitState(gitStates, sid, agent.AgentName))
		addCapabilities(entry, capabilitiesMap[sid])
		addContextUsage(entry, ctxModelMap[sid], latestTurnCtx[sid])
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
			"name":                  ls.AgentName,
			"agent_type":            ls.AgentType,
			"session_id":            ls.SessionID,
			"tmux_session":          nil,
			"status":                "Sleeping",
			"summary":               nil,
			"staleness_seconds":     nil,
			"working_directory":     ls.WorkingDir,
			"display_name":          dn,
			"icon":                  ls.Icon,
			"name_color":            ls.NameColor,
			"branch":                nil,
			"waiting_for_input":     false,
			"awaiting_user":         false,
			"not_started":           false,
			"done":                  false,
			"waiting_reason":        nil,
			"waiting_summary":       nil,
			"working":               false,
			"stuck":                 false,
			"changed_file_count":    0,
			"commands":              map[string]string{"compress": "/compact", "clear": "/clear"},
			"board_project":         bp,
			"board_job_title":       dn,
			"board_unread":          0,
			"board_is_orchestrator": nil,
			"log_path":              "",
			"sleeping":              true,
			"archived":              ls.IsArchived == 1,
			"first_prompt":          h.jsonl.FirstUserPrompt(ls.SessionID, ls.WorkingDir, ls.AgentType),
		})
		addContextUsage(sessions[len(sessions)-1], nil, 0)
		addCapabilities(sessions[len(sessions)-1], ls.Capabilities)
		for k, v := range autoAcceptFields(ls.AgentType, store.AutoAcceptState{}) {
			sessions[len(sessions)-1][k] = v // asleep: always off
		}
	}

	return archivedLast(sessions), nil
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
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) { // a bare name could attach to (and type into) another agent's terminal (#3211)
		if conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r)); err == nil {
			conn.Close(websocket.StatusPolicyViolation, "session_id is required")
		}
		return
	}
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, true); status != 0 {
			conn, err := websocket.Accept(w, r, h.wsAcceptOptions(r))
			if err == nil {
				conn.Close(websocket.StatusPolicyViolation, message)
			}
			return
		}
	}

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
		// The terminal is already gone: say so as when it ends while open, so the client
		// shows the session ended instead of retrying a broken connection (#3276).
		if errors.Is(err, ptymanager.ErrTerminalGone) {
			wsjson.Write(ctx, conn, map[string]any{"type": "terminal_closed"})
			conn.Close(websocket.StatusNormalClosure, "session ended")
			return
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

	// This server answers every terminal_input that carries an id (#3276): the client then
	// settles a send on the answer instead of on a quiet window. Sent after the replay seed,
	// which clients expect first.
	wsjson.Write(ctx, conn, map[string]any{"type": "terminal_hello", "input_acks": true})

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
				// ID, on terminal_input, asks for an answer carrying it (#3276): a
				// terminal_input_ack once the input is delivered, or the refusal with the id.
				// Decoded leniently: a frame whose id is not an integer is still typed, as
				// before ids existed, and gets no answer.
				RawID json.RawMessage `json:"id"`
				ID    *int64          `json:"-"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			if len(msg.RawID) > 0 {
				var id int64
				if json.Unmarshal(msg.RawID, &id) == nil {
					msg.ID = &id
				}
			}
			// Input or a resize for a terminal that is gone is not dropped silently: the
			// client is told, like the 409 terminal_gone of POST /send and /keys (#3218).
			var sendErr error
			switch msg.Type {
			case "terminal_input":
				if msg.Data != "" {
					sendErr = h.backend.SendInput(name, []byte(msg.Data))
				}
			case "terminal_resize":
				if msg.Cols >= 10 {
					rows := uint16(msg.Rows)
					if rows == 0 {
						rows = 50
					}
					sendErr = h.backend.Resize(name, uint16(msg.Cols), rows)
				}
			}
			withID := func(frame map[string]any) map[string]any {
				if msg.Type == "terminal_input" && msg.ID != nil {
					frame["id"] = *msg.ID
				}
				return frame
			}
			switch {
			case errors.Is(sendErr, ptymanager.ErrTerminalGone):
				wsjson.Write(ctx, conn, withID(map[string]any{"type": "terminal_gone", "code": "terminal_gone", "error": terminalGoneMessage, "not_sent": msg.Type == "terminal_input"}))
			case msg.Type != "terminal_input" || msg.ID == nil:
				// Resizes and input without an id get no answer (older clients).
			case sendErr != nil:
				wsjson.Write(ctx, conn, withID(map[string]any{"type": "terminal_input_failed", "error": "The input could not be sent to the agent's terminal.", "not_sent": true}))
			default:
				wsjson.Write(ctx, conn, withID(map[string]any{"type": "terminal_input_ack"}))
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
