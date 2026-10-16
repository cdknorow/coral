package routes

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/logparse"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// List returns all live agent sessions with enriched metadata.
// GET /api/sessions/live
func (h *SessionsHandler) List(w http.ResponseWriter, r *http.Request) {
	trace := newPhaseTrace("live-list")
	defer trace.finish()
	trace.begin("terminal_discovery")
	agents, err := h.discoverAgents(r)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	trace.end("terminal_discovery", "agents", len(agents))

	ctx := r.Context()
	allLive, err := h.ss.GetAllLiveSessions(ctx)
	if err != nil {
		slog.Warn("failed to list live session metadata", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Agent metadata is temporarily unavailable; try again.", "code": "agent_metadata_unavailable"})
		return
	}
	liveSleeping := make(map[string]bool)
	liveArchived := make(map[string]bool)
	autoAcceptStates, err := h.ss.GetAutoAcceptStates(ctx)
	if err != nil {
		slog.Warn("failed to read auto-accept states", "error", err)
	}
	for _, session := range allLive {
		liveSleeping[session.SessionID] = session.IsSleeping == 1
		liveArchived[session.SessionID] = session.IsArchived == 1
	}
	// A sleeping row can briefly outlive its terminal during shutdown. Use
	// the stored placeholder below instead of scanning that terminal's files.
	awakeAgents := agents[:0]
	for _, agent := range agents {
		if !liveSleeping[agent.SessionID] {
			awakeAgents = append(awakeAgents, agent)
		}
	}
	agents = awakeAgents

	// Batch fetch enrichment data
	sessionIDs := make([]string, 0, len(agents))
	sessionNames := make([]string, 0, len(agents))
	sessionAgents := make(map[string]string, len(agents))
	for _, a := range agents {
		if a.SessionID != "" && !liveSleeping[a.SessionID] {
			sessionIDs = append(sessionIDs, a.SessionID)
			sessionNames = append(sessionNames, a.TmuxSession)
			sessionAgents[a.SessionID] = a.AgentName
		}
	}
	// The enrichment reads don't depend on each other: they run concurrently, a few at
	// a time, on the read pools (#3461). Run one after another they summed every
	// connection wait, and under write load the live list's p95 reached ~1 s.
	trace.begin("db_reads")
	var (
		displayNames, icons, nameColors, latestGoals map[string]string
		displayErr, subsErr, goalsErr, unreadErr     error
		gitState                                     map[string]*store.GitSnapshot
		fileCounts                                   map[string]int
		latestEvents                                 map[string][2]string
		stateEvents                                  map[string][]store.AgentEvent
		tokenUsageMap                                map[string]*store.TokenUsage
		latestTurnCtx                                map[string]int
		boardSubs                                    map[string]*board.Subscriber
		allUnread                                    map[string]int
	)
	reads := []func(){
		func() { displayNames, displayErr = h.ss.GetDisplayNames(ctx, sessionIDs) },
		func() { icons, _ = h.ss.GetIcons(ctx, sessionIDs) },
		func() { nameColors, _ = h.ss.GetNameColors(ctx, sessionIDs) },
		func() { gitState, _ = h.gs.GetLiveGitState(ctx, sessionAgents) },
		func() { fileCounts, _ = h.ts.GetEditedFileCounts(ctx, sessionIDs) },
		func() { latestEvents, _ = h.ts.GetLatestEventTypes(ctx, sessionIDs) },
		func() { stateEvents, _ = h.ts.GetSessionStateEvents(ctx, sessionIDs) },
		func() { latestGoals, goalsErr = h.ts.GetLatestGoals(ctx, sessionIDs) },
	}
	if h.tokenStore != nil && len(sessionIDs) > 0 {
		reads = append(reads,
			func() { tokenUsageMap, _ = h.tokenStore.GetLatestUsageBySessionIDs(ctx, sessionIDs) },
			func() { latestTurnCtx, _ = h.tokenStore.GetLatestTurnContextBySessionIDs(ctx, sessionIDs) })
	}
	if h.bs != nil {
		reads = append(reads,
			func() { boardSubs, subsErr = h.bs.GetAllSubscriptions(ctx) },
			func() { allUnread, unreadErr = h.bs.GetUnreadCountsForSessions(ctx, sessionNames) })
	}
	runBounded(listReadConcurrency, reads)
	trace.endPool("db_reads", h.db, "reads", len(reads))
	if displayErr != nil {
		slog.Warn("failed to read live session identities", "error", displayErr)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Agent metadata is temporarily unavailable; try again.", "code": "agent_metadata_unavailable"})
		return
	}
	if subsErr != nil {
		slog.Warn("failed to get board subscriptions", "error", subsErr)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Agent subscriptions are temporarily unavailable; try again.", "code": "agent_metadata_unavailable"})
		return
	}
	if goalsErr != nil {
		slog.Warn("failed to get latest goals", "error", goalsErr)
	}
	if unreadErr != nil {
		slog.Warn("failed to get board unread counts", "error", unreadErr)
	}
	if icons == nil {
		icons = map[string]string{}
	}
	if gitState == nil {
		gitState = map[string]*store.GitSnapshot{}
	}
	if fileCounts == nil {
		fileCounts = map[string]int{}
	}
	if latestEvents == nil {
		latestEvents = map[string][2]string{}
	}
	if stateEvents == nil {
		stateEvents = map[string][]store.AgentEvent{}
	}
	// Launch times, used to bound how long after launch a stalled start is
	// reported. See agentNeverStarted.
	createdAtMap := map[string]string{}
	for _, ls := range allLive {
		createdAtMap[ls.SessionID] = ls.CreatedAt
	}
	if tokenUsageMap == nil {
		tokenUsageMap = map[string]*store.TokenUsage{}
	}
	if latestTurnCtx == nil {
		latestTurnCtx = map[string]int{}
	}
	if latestGoals == nil {
		latestGoals = map[string]string{}
	}
	if boardSubs == nil {
		boardSubs = map[string]*board.Subscriber{}
	}
	if allUnread == nil {
		allUnread = map[string]int{}
	}

	// Fallback: board_name from live_sessions DB for agents not yet subscribed
	liveBoardNames := make(map[string][2]string) // session_id -> [board_name, display_name]
	type liveExtra struct {
		Prompt        *string
		Model         *string
		Capabilities  *string
		ContextWindow int
	}
	liveExtras := make(map[string]liveExtra) // session_id -> extra fields
	trace.begin("db_live_session_extras")
	// Use the authoritative snapshot already read above. A second, ignored
	// query failure used to erase team names and other identity metadata.
	for _, session := range allLive {
		if session.BoardName != nil {
			liveBoardNames[session.SessionID] = [2]string{*session.BoardName, ptr.Value(session.DisplayName)}
		}
		liveExtras[session.SessionID] = liveExtra{
			Prompt: session.Prompt, Model: session.Model, Capabilities: session.Capabilities, ContextWindow: session.ContextWindow,
		}
	}
	trace.endPool("db_live_session_extras", h.db)

	var sessions []map[string]any
	liveSIDs := make(map[string]bool) // track session IDs already in results
	// Keep the response path bounded for large teams: cold log enrichment is
	// warmed asynchronously and appears on the next sidebar refresh.
	largeList := len(agents) > 8
	trace.begin("state_transcript_enrichment")
	for _, agent := range agents {
		agentStarted := time.Now()
		var logInfo map[string]any
		sleeping := liveSleeping[agent.SessionID]
		logReady := true
		if sleeping {
			logInfo = getLogStatus("")
		} else if largeList {
			var ready bool
			logInfo, ready = h.readyLogStatus(agent.LogPath)
			if !ready {
				logReady = false
				h.warmLogStatus(agent.LogPath)
				logInfo = getLogStatus("")
			}
		} else {
			logInfo = h.cachedLogStatus(agent.LogPath)
		}
		trace.agentDetail("log_status", agent.SessionID, time.Since(agentStarted))
		if largeList && !sleeping {
			h.warmTranscript(agent)
		}
		skipExpensive := sleeping || largeList && (!logReady || !h.transcriptReady(agent.SessionID))

		staleness := logInfo["staleness_seconds"]

		sid := agent.SessionID

		git := lookupGitState(gitState, sid, agent.AgentName)

		// Resolve changed file count (from Write/Edit tool events)
		fc := 0
		if sid != "" {
			if c, ok := fileCounts[sid]; ok {
				fc = c
			}
		}

		// Resolve latest event type for waiting/working detection
		var latestEv string
		if sid != "" {
			if ev, ok := latestEvents[sid]; ok {
				latestEv = ev[0]
			}
		}
		staleF, _ := staleness.(float64)
		notStarted := agentNeverStarted(agent.AgentType, latestEv, staleF, sessionAgeSeconds(createdAtMap[sid]))
		stateInput := SessionStateInput{StalenessSeconds: staleF, NotStarted: notStarted, Sleeping: liveSleeping[sid], Archived: liveArchived[sid]}
		for _, ev := range stateEvents[sid] {
			stateInput.Events = append(stateInput.Events, ev.StateEvent())
		}
		transcriptStarted := time.Now()
		if !skipExpensive {
			h.applyTranscriptState(&stateInput, stateEvents[sid], agent.AgentType, agent.TmuxSession, sid, agent.WorkingDir)
		}
		trace.agentDetail("transcript_state", sid, time.Since(transcriptStarted))
		state := DeriveSessionState(stateInput)

		// The goal line is the newest goal event (the goal generator or an
		// operator edit).
		summary := latestGoals[sid]

		// Board unread
		tmuxName := agent.TmuxSession
		boardSub := boardSubs[tmuxName]
		boardUnread := 0
		if boardSub != nil {
			boardUnread = allUnread[tmuxName]
		}

		branchVal, repoNameVal, worktreeVal := gitListFields(git)

		statusVal, summaryVal, displayVal, iconVal := listTextFields(logInfo, summary, displayNames, icons, sid)

		firstPrompt := h.jsonl.CachedFirstUserPrompt(sid)
		if !skipExpensive {
			promptStarted := time.Now()
			firstPrompt = h.jsonl.FirstUserPrompt(sid, agent.WorkingDir, agent.AgentType)
			trace.agentDetail("first_prompt", sid, time.Since(promptStarted))
		}
		entry := map[string]any{
			"name":                  agent.AgentName,
			"agent_type":            agent.AgentType,
			"session_id":            sid,
			"tmux_session":          agent.TmuxSession,
			"status":                statusVal,
			"summary":               summaryVal,
			"staleness_seconds":     listStaleness(staleness),
			"working_directory":     agent.WorkingDir,
			"display_name":          displayVal,
			"icon":                  iconVal,
			"name_color":            ptr.NullIfEmpty(nameColors[sid]),
			"branch":                branchVal,
			"repo_name":             repoNameVal,
			"git_state":             worktreeVal,
			"waiting_for_input":     state.NeedsInput,
			"needs_login":           state.NeedsLogin,
			"awaiting_user":         state.AwaitingUser,
			"not_started":           state.NotStarted,
			"done":                  state.Done,
			"waiting_reason":        nilIf(!state.NeedsInput, state.WaitingReason),
			"waiting_summary":       nilIf(!state.NeedsInput, state.WaitingSummary),
			"waiting_kind":          nilIf(!state.NeedsInput || state.WaitingKind == "", state.WaitingKind),
			"working":               state.Working,
			"stuck":                 state.Stuck,
			"changed_file_count":    fc,
			"commands":              map[string]string{"compress": "/compact", "clear": "/clear"},
			"board_project":         boardProject(boardSubs, liveBoardNames, tmuxName, sid),
			"board_job_title":       boardJobTitle(boardSubs, liveBoardNames, tmuxName, sid),
			"board_unread":          boardUnread,
			"held_inputs":           ptymanager.HeldCount(tmuxName), // automated sends the gate holds (#3578)
			"board_is_orchestrator": boardSubscriberFlag(boardSub),
			"log_path":              agent.LogPath,
			"sleeping":              liveSleeping[sid],
			"archived":              liveArchived[sid],
			"first_prompt":          firstPrompt,
			"enrichment_pending":    skipExpensive && !sleeping,
		}
		// Always present (false/""/null too): the dashboard merges entries, so
		// an omitted false would leave the AUTO chip on after it was turned off.
		for k, v := range autoAcceptFields(agent.AgentType, autoAcceptStates[sid]) {
			entry[k] = v
		}
		// Include prompt, model, and capabilities from live_sessions DB
		if extra, ok := liveExtras[sid]; ok {
			if extra.Prompt != nil {
				entry["prompt"] = *extra.Prompt
			}
			if extra.Model != nil && *extra.Model != "" {
				entry["model"] = *extra.Model
			}
			addCapabilities(entry, extra.Capabilities)
		}

		// Include token usage data if available
		if usage, ok := tokenUsageMap[sid]; ok {
			entry["token_input"] = usage.InputTokens
			entry["token_output"] = usage.OutputTokens
			entry["token_cache_read"] = usage.CacheReadTokens
			entry["token_cache_write"] = usage.CacheWriteTokens
			entry["token_cost_usd"] = usage.CostUSD
		}

		// Context window usage: latest turn's input_tokens / context_window.
		// Explicit nulls clear stale values in clients that spread-merge updates.
		var contextModel *string
		if extra, ok := liveExtras[sid]; ok {
			contextModel = extra.Model
		}
		addContextUsage(entry, contextModel, latestTurnCtx[sid])

		if sid != "" {
			liveSIDs[sid] = true
		}
		sessions = append(sessions, entry)
	}
	trace.end("state_transcript_enrichment", "rows", len(sessions), "large_list", largeList)

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
			"waiting_kind":          nil,
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
			"sleep_reason":          sleepReason(ls),
			"first_prompt":          h.sleepingFirstPrompt(ls),
		})
		// Include prompt, model, and capabilities for sleeping sessions too
		entry := sessions[len(sessions)-1]
		addContextUsage(entry, nil, 0)
		if ls.Prompt != nil {
			entry["prompt"] = *ls.Prompt
		}
		if ls.Model != nil && *ls.Model != "" {
			entry["model"] = *ls.Model
		}
		addCapabilities(entry, ls.Capabilities)
		for k, v := range autoAcceptFields(ls.AgentType, store.AutoAcceptState{}) {
			entry[k] = v // asleep: always off
		}
	}

	h.addBoardLabels(r.Context(), sessions)
	sessions = archivedLast(sessions)
	// Polled by every open dashboard: an unchanged list is a 304 with no body (#3439).
	if snap := h.remoteSnapshot(); snap.Configured() {
		httputil.WriteJSONValidated(w, r, http.StatusOK, map[string]any{
			"sessions": mergeRemote(sessions, snap), // each remote sorts its own rows
			"servers":  snap.Servers,
		})
		return
	}
	httputil.WriteJSONValidated(w, r, http.StatusOK, emptyIfNil(sessions))
}

// ResolveSession returns the authoritative routing and identity metadata for a
// stable session ID. Unknown UUIDs deliberately return a not_found state with
// HTTP 200 so a freshly opened popout can retry while the session index catches
// up without ever falling back to an agent name.
func (h *SessionsHandler) ResolveSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	parsed, err := uuid.Parse(sessionID)
	if err != nil || parsed.String() != sessionID {
		errBadRequest(w, "invalid session_id")
		return
	}

	ls, err := h.ss.GetLiveSession(r.Context(), sessionID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if ls == nil {
		var indexed bool
		if err := h.db.Reader().GetContext(r.Context(), &indexed,
			"SELECT EXISTS(SELECT 1 FROM session_index WHERE session_id = ?)", sessionID); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		state := "not_found"
		if indexed {
			state = "finished"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"session_id":        sessionID,
			"state":             state,
			"active":            false,
			"sleeping":          false,
			"agent_type":        nil,
			"name":              nil,
			"tmux_session":      nil,
			"working_directory": nil,
			"display_name":      nil,
			"auto_name":         nil,
			"board_project":     nil,
			"board_job_title":   nil,
			"icon":              nil,
			"status":            nil,
			"summary":           nil,
			"first_prompt":      nil,
			"waiting_for_input": false,
			"waiting_reason":    nil,
			"waiting_summary":   nil,
			"waiting_kind":      nil,
			"working":           false,
			"done":              false,
			"stuck":             false,
			"not_started":       false,
		})
		return
	}

	var status string
	if err := h.db.Reader().GetContext(r.Context(), &status,
		"SELECT status FROM live_sessions WHERE session_id = ?", sessionID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	active := status == "active"
	sleeping := active && ls.IsSleeping == 1
	state := "finished"
	if sleeping {
		state = "sleeping"
	} else if active {
		state = "active"
	}
	var tmuxSession any
	if active && !sleeping {
		tmuxSession = naming.SessionName(ls.AgentType, ls.SessionID)
	}
	logInfo := getLogStatus("")
	firstPrompt := h.sleepingFirstPrompt(*ls)
	if !sleeping {
		logInfo = h.cachedLogStatus(naming.LogFile(h.cfg.LogDir, ls.AgentType, ls.SessionID))
		firstPrompt = h.jsonl.FirstUserPrompt(ls.SessionID, ls.WorkingDir, ls.AgentType)
	}
	initialStatus, _ := logInfo["status"].(string)
	summary := ""
	// Same derivation as the list and WebSocket rows. The popout merges this
	// record over the live row, so the state fields must agree with it.
	var liveState SessionState
	if active && !sleeping {
		staleness, _ := logInfo["staleness_seconds"].(float64)
		liveState = h.deriveSingleSessionState(r.Context(), sessionID, staleness)
	}
	out := map[string]any{
		"session_id":        ls.SessionID,
		"state":             state,
		"active":            active,
		"sleeping":          sleeping,
		"archived":          sleeping && ls.IsArchived == 1,
		"agent_type":        ls.AgentType,
		"name":              ls.AgentName,
		"tmux_session":      tmuxSession,
		"working_directory": ls.WorkingDir,
		"display_name":      ls.DisplayName,
		"auto_name":         nil,
		"board_project":     ls.BoardName,
		"board_label":       h.boardLabel(ptr.Value(ls.BoardName)),
		"board_job_title":   ls.DisplayName,
		"icon":              ls.Icon,
		"name_color":        ls.NameColor,
		"status":            ptr.NullIfEmpty(initialStatus),
		"summary":           ptr.NullIfEmpty(summary),
		"first_prompt":      firstPrompt,
		"waiting_for_input": liveState.NeedsInput,
		"awaiting_user":     liveState.AwaitingUser,
		"waiting_reason":    ptr.NullIfEmpty(liveState.WaitingReason),
		"waiting_summary":   ptr.NullIfEmpty(liveState.WaitingSummary),
		"waiting_kind":      ptr.NullIfEmpty(liveState.WaitingKind),
		"working":           liveState.Working,
		"done":              false,
		"stuck":             false,
		"not_started":       false,
	}
	for k, v := range h.autoAcceptPayload(r.Context(), ls.SessionID) {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// validateExactLiveTarget prevents name-based terminal APIs from selecting a
// sibling session. The route name is checked against metadata loaded by the
// supplied session ID; agentType is a consistency assertion, never a lookup key.
func (h *SessionsHandler) validateExactLiveTarget(ctx context.Context, name, agentType, sessionID string, terminalRoute bool) (*store.LiveSession, int, string) {
	if sessionID == "" || agentType == "" {
		return nil, http.StatusBadRequest, errSessionIDRequired
	}
	parsed, err := uuid.Parse(sessionID)
	if err != nil || parsed.String() != sessionID {
		return nil, http.StatusBadRequest, "invalid session_id"
	}
	ls, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil {
		slog.Warn("failed to validate live session metadata", "session_id", sessionID, "error", err)
		return nil, http.StatusServiceUnavailable, "Agent metadata is temporarily unavailable; try again."
	}
	if ls == nil {
		return nil, http.StatusNotFound, "live session not found"
	}
	var status string
	if err := h.db.Reader().GetContext(ctx, &status,
		"SELECT status FROM live_sessions WHERE session_id = ?", sessionID); err != nil {
		slog.Warn("failed to validate live session metadata", "session_id", sessionID, "error", err)
		return nil, http.StatusServiceUnavailable, "Agent metadata is temporarily unavailable; try again."
	}
	if status != "active" {
		return nil, http.StatusNotFound, "live session not found"
	}
	if ls.IsArchived == 1 {
		return nil, http.StatusConflict, "session is archived"
	}
	if ls.IsSleeping == 1 {
		return nil, http.StatusConflict, "session is sleeping"
	}
	expectedName := ls.AgentName
	if terminalRoute {
		expectedName = naming.SessionName(ls.AgentType, ls.SessionID)
	}
	nameMatches := name == expectedName
	if !terminalRoute {
		// Launch responses and existing API clients use the canonical terminal
		// name as the REST path, while the dashboard uses the stored agent name.
		// Both are unambiguous only after the exact type/ID tuple has matched.
		nameMatches = nameMatches || name == naming.SessionName(ls.AgentType, ls.SessionID)
	}
	if agentType != ls.AgentType || !nameMatches {
		return nil, http.StatusBadRequest, "session target mismatch"
	}
	return ls, 0, ""
}

// errSessionIDRequired is the message for a request that does not name its exact
// target. It answers 400 with code session_id_required, never 404: through the
// hub a 404 reads as a route refusal.
const errSessionIDRequired = "session_id and agent_type are required"

func writeExactTargetError(w http.ResponseWriter, status int, message string) {
	body := map[string]string{"error": message}
	if message == errSessionIDRequired {
		body["code"] = "session_id_required"
	}
	writeJSON(w, status, body)
}

// terminalGoneMessage is shown in the composer, prompt bar and keys (#3211).
const terminalGoneMessage = "The agent's terminal is gone"

// writeTerminalError answers a terminal write or action that failed: 409 terminal_gone when
// the agent's own terminal no longer exists (nothing was sent anywhere, #3211), else 500.
func writeTerminalError(w http.ResponseWriter, err error) {
	if errors.Is(err, ptymanager.ErrTerminalGone) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": terminalGoneMessage, "code": "terminal_gone", "not_sent": true})
		return
	}
	errInternalServer(w, err.Error())
}

// namesNoSession is true when a request has no session id and its name is not an exact
// "type-<uuid>" terminal name (which names one session; the terminal resolves it by id).
func namesNoSession(name, sessionID string) bool {
	if sessionID != "" {
		return false
	}
	_, sid := logparse.ParseSessionName(name)
	return sid == ""
}

// writeSessionIDRequired refuses a per-session request that names no session id: by bare
// name it would reach whichever terminal matches the name, another agent's included (#3211).
func writeSessionIDRequired(w http.ResponseWriter) {
	// (Callers check namesNoSession first: an exact "type-<uuid>" terminal name names one session.)
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id is required", "code": "session_id_required"})
}

func shouldValidateExactTarget(agentType, sessionID string) bool {
	return agentType != "" || sessionID != ""
}

// ResolveByPIDs looks up the agent identity for a set of process IDs: the caller's process
// and its ancestors, nearest first. Used by coral-board to identify itself when env vars are
// stripped (e.g. Codex sandbox).
// GET /api/sessions/resolve?pids=12345,12340,12335
func (h *SessionsHandler) ResolveByPIDs(w http.ResponseWriter, r *http.Request) {
	pidStr := r.URL.Query().Get("pids")
	if pidStr == "" {
		errBadRequest(w, "pids parameter required")
		return
	}
	var pids []int
	for _, s := range strings.Split(pidStr, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		pid, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	if len(pids) == 0 {
		errBadRequest(w, "no valid PIDs")
		return
	}
	rows, err := h.ss.LiveSessionsByPIDs(r.Context(), pids)
	var ls *store.LiveSession
	if err == nil {
		ls = h.verifiedSessionForPIDs(r.Context(), pids, rows)
	}
	if ls == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no matching session"})
		return
	}
	role := ""
	if ls.DisplayName != nil {
		role = *ls.DisplayName
	}
	boardName := ""
	if ls.BoardName != nil {
		boardName = *ls.BoardName
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"subscriber_id": naming.SubscriberID(role, ls.AgentType),
		"project":       boardName,
		"session_name":  naming.SessionName(ls.AgentType, ls.SessionID),
	})
}

// verifiedSessionForPIDs returns the session the caller runs in, or nil. pids are the
// caller's process and its ancestors, nearest first; rows are the active sessions whose
// recorded process is one of them. The first pid that is a session's process NOW, as its
// terminal backend reports it, names the session. A recorded pid that is no longer the
// session's process is skipped: the number may have been reused by anything, another
// agent's shell included, and trusting it handed that agent this session's board and role
// (#3864). Two sessions on one pid give no answer rather than a guess.
func (h *SessionsHandler) verifiedSessionForPIDs(ctx context.Context, pids []int, rows []store.LiveSession) *store.LiveSession {
	byPID := make(map[int][]*store.LiveSession, len(rows))
	for i := range rows {
		byPID[rows[i].PID] = append(byPID[rows[i].PID], &rows[i])
	}
	for _, pid := range pids {
		var live []*store.LiveSession
		for _, ls := range byPID[pid] {
			backend := ""
			if ls.Backend != nil {
				backend = *ls.Backend
			}
			now, err := launchedSessionPID(ctx, backend, h.backend, h.terminal, naming.SessionName(ls.AgentType, ls.SessionID))
			if err == nil && now == pid {
				live = append(live, ls)
			}
		}
		switch len(live) {
		case 0:
			continue
		case 1:
			return live[0]
		default:
			return nil
		}
	}
	return nil
}

// listReadConcurrency bounds the live list's concurrent reads: the size of the read
// pools (#3441), so one list doesn't queue more than they serve at once (#3461).
const listReadConcurrency = 4

// runBounded runs fns at most limit at a time and waits for all of them. Each fn
// writes only its own results.
func runBounded(limit int, fns []func()) {
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		sem <- struct{}{}
		go func(fn func()) {
			defer func() { <-sem; wg.Done() }()
			fn()
		}(fn)
	}
	wg.Wait()
}
