package routes

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/logparse"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/store"
)

// List returns all live agent sessions with enriched metadata.
// GET /api/sessions/live
func (h *SessionsHandler) List(w http.ResponseWriter, r *http.Request) {
	trace := newPhaseTrace("live-list")
	defer trace.finish()
	sessions, err := h.liveSessionRows(r, trace, false)
	var unavailable *listUnavailable
	switch {
	case errors.As(err, &unavailable):
		writeError(w, http.StatusServiceUnavailable, "agent_metadata_unavailable", unavailable.msg)
		return
	case err != nil:
		errInternalServer(w, err.Error())
		return
	}
	h.orderLiveRows(r.Context(), sessions)
	// An agent's token sees the roster of other boards' sessions, not what they work on (#5041), and nothing of the registered remote servers.
	owner, isAgent := VerifiedSession(r.Context())
	if isAgent {
		sessions = scopeRowsForAgent(sessions, owner.SessionID)
	}
	// Polled by every open dashboard: an unchanged list is a 304 with no body (#3439).
	if snap := h.remoteSnapshot(); snap.Configured() {
		if isAgent {
			httputil.WriteJSONValidated(w, r, http.StatusOK, map[string]any{"sessions": mergeRemote(sessions, background.RemoteSnapshot{}), "servers": []any{}})
			return
		}
		httputil.WriteJSONValidated(w, r, http.StatusOK, map[string]any{
			"sessions": mergeRemote(sessions, snap), // each remote sorts its own rows
			"servers":  snap.Servers,
		})
		return
	}
	httputil.WriteJSONValidated(w, r, http.StatusOK, emptyIfNil(sessions))
}

// orderLiveRows puts the live list's rows in a stable order and gives each the time its place in the list was taken, "list_at"
// (#4943). The terminals list their sessions in no order that lasts (the pty backend's are a map's), and the dashboard shows the
// list in the order it is given, so an agent's place, and its team's (the earliest of its agents'), came from where discovery
// happened to put it, and a feed diff adds a new session last. The order is list_at, then the id; a restarted or resumed agent keeps
// the list_at of the session it replaces (store.ReplaceLiveSession), so it stays where it was, and a new agent or team comes last.
// Awake agents come first, then sleeping, then archived ones, as before. A row with no live record goes last in its kind. If the times cannot be read the rows stay as they were.
func (h *SessionsHandler) orderLiveRows(ctx context.Context, rows []map[string]any) {
	if h.ss == nil || len(rows) == 0 {
		return
	}
	at, err := h.ss.LiveListAt(ctx)
	if err != nil {
		return
	}
	id := func(row map[string]any) string { s, _ := row["session_id"].(string); return s }
	for _, row := range rows {
		if t, ok := at[id(row)]; ok {
			row["list_at"] = t
		}
	}
	// Awake agents first, then sleeping ones, then archived ones, as the list always had them; each kind by place.
	kind := func(row map[string]any) int {
		if archived, _ := row["archived"].(bool); archived {
			return 2
		}
		if sleeping, _ := row["sleeping"].(bool); sleeping {
			return 1
		}
		return 0
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if ki, kj := kind(rows[i]), kind(rows[j]); ki != kj {
			return ki < kj
		}
		ai, aok := at[id(rows[i])]
		aj, bok := at[id(rows[j])]
		if aok != bok {
			return aok
		}
		if ai != aj {
			return ai < aj
		}
		return id(rows[i]) < id(rows[j])
	})
}

// addTeamHealth gives each row of a team its team's health check state, "team_health_on" (#4962): the on/off icon beside the team's
// info button reads it. The check's settings are the local dashboard's, so a request through Coral Hub gets no field (as the board
// settings GET leaves "health" out, #4176 rule 17) and the dashboard draws no icon there.
//
// The state must not flip with a failed read (#5119: the icon blinked): when the settings cannot be read for a tick (a busy database, a
// request that ended), the last state that was read stands, and with none yet the rows carry no field (the dashboard keeps what it has).
func (h *SessionsHandler) addTeamHealth(r *http.Request, rows []map[string]any) {
	if h.bs == nil || cloud.Tunneled(r) {
		return
	}
	var on map[string]bool
	for _, row := range rows {
		project, _ := row["board_project"].(string)
		if project == "" {
			continue
		}
		if on == nil {
			var err error
			if on, err = h.bs.HealthOnByProject(r.Context()); err != nil {
				var known bool
				if on, known = h.healthOn.last(); !known {
					return
				}
			} else {
				h.healthOn.set(on)
			}
		}
		v, stored := on[project]
		if !stored {
			v = board.DefaultHealthSettings().On
		}
		row["team_health_on"] = v
	}
}

// healthOnMemo is the last health state read of every team (addTeamHealth).
type healthOnMemo struct {
	mu sync.Mutex
	m  map[string]bool // read only once stored: set replaces it
}

func (c *healthOnMemo) set(m map[string]bool) {
	c.mu.Lock()
	c.m = m
	c.mu.Unlock()
}

func (c *healthOnMemo) last() (map[string]bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m, c.m != nil
}

// listUnavailable is a live list that can't be built right now: the agents' metadata could not be read.
type listUnavailable struct{ msg string }

func (e *listUnavailable) Error() string { return e.msg }

var (
	errListMetadata      = &listUnavailable{"Agent metadata is temporarily unavailable; try again."}
	errListSubscriptions = &listUnavailable{"Agent subscriptions are temporarily unavailable; try again."}
)

// liveSessionRows is the live list's rows, archived last: what /api/sessions/live and the /ws/coral feed both send
// (#4599: the feed had its own copy of this, which had drifted from the list's). A metadata read that fails is a
// listUnavailable; the list answers it 503, and the feed skips that tick. full enriches every row now (the feed:
// a tick's rows are compared with the last tick's, #4308, so they must not depend on background warming); without
// it a list of more than 8 agents leaves cold rows' log and transcript work to the background and marks them
// enrichment_pending (the HTTP list).
func (h *SessionsHandler) liveSessionRows(r *http.Request, trace *phaseTrace, full bool) ([]map[string]any, error) {
	trace.begin("terminal_discovery")
	agents, err := h.discoverAgents(r)
	if err != nil {
		return nil, err
	}
	trace.end("terminal_discovery", "agents", len(agents))

	ctx := r.Context()
	allLive, err := h.ss.GetAllLiveSessions(ctx)
	if err != nil {
		slog.Warn("failed to list live session metadata", "error", err)
		return nil, errListMetadata
	}
	autoAcceptStates, err := h.ss.GetAutoAcceptStates(ctx)
	if err != nil {
		slog.Warn("failed to read auto-accept states", "error", err)
	}
	lv := newLiveIndex(allLive)
	// A sleeping row can briefly outlive its terminal during shutdown. Use
	// the stored placeholder below instead of scanning that terminal's files.
	awakeAgents := agents[:0]
	for _, agent := range agents {
		if !lv.sleeping[agent.SessionID] {
			awakeAgents = append(awakeAgents, agent)
		}
	}
	agents = awakeAgents

	d, err := h.readListData(ctx, trace, agents, lv)
	if err != nil {
		return nil, err
	}
	trace.begin("db_live_session_extras")
	trace.endPool("db_live_session_extras", h.db)

	var sessions []map[string]any
	liveSIDs := make(map[string]bool) // track session IDs already in results
	// Keep the response path bounded for large teams: cold log enrichment is
	// warmed asynchronously and appears on the next sidebar refresh.
	largeList := !full && len(agents) > 8
	trace.begin("state_transcript_enrichment")
	for _, agent := range agents {
		entry := h.agentRow(trace, agent, lv, d, autoAcceptStates, largeList)
		if agent.SessionID != "" {
			liveSIDs[agent.SessionID] = true
		}
		sessions = append(sessions, entry)
	}
	trace.end("state_transcript_enrichment", "rows", len(sessions), "large_list", largeList)

	sessions = append(sessions, h.sleepingRows(allLive, liveSIDs)...)
	h.addBoardLabels(r.Context(), sessions)
	h.addTeamHealth(r, sessions)
	return archivedLast(sessions), nil
}

// liveExtra is what a live session's row adds from its stored metadata.
type liveExtra struct {
	Prompt        *string
	Model         *string
	Thinking      *string
	Observed      *string
	Capabilities  *string
	ContextWindow int
}

// liveIndex is the stored live sessions by session id: asleep, archived, launched when, the board (fallback for
// agents not yet subscribed: board name and display name) and the extra fields.
type liveIndex struct {
	sleeping, archived map[string]bool
	createdAt          map[string]string
	boardNames         map[string][2]string
	extras             map[string]liveExtra
}

// newLiveIndex indexes the authoritative snapshot of the live sessions (a second, ignored query failure used to
// erase team names and other identity metadata).
func newLiveIndex(allLive []store.LiveSession) liveIndex {
	lv := liveIndex{sleeping: map[string]bool{}, archived: map[string]bool{}, createdAt: map[string]string{},
		boardNames: map[string][2]string{}, extras: map[string]liveExtra{}}
	for _, session := range allLive {
		lv.sleeping[session.SessionID] = session.IsSleeping == 1
		lv.archived[session.SessionID] = session.IsArchived == 1
		// Launch times, used to bound how long after launch a stalled start is
		// reported. See agentNeverStarted.
		lv.createdAt[session.SessionID] = session.CreatedAt
		if session.BoardName != nil {
			lv.boardNames[session.SessionID] = [2]string{*session.BoardName, ptr.Value(session.DisplayName)}
		}
		lv.extras[session.SessionID] = liveExtra{
			Prompt: session.Prompt, Model: session.Model, Thinking: session.Thinking, Observed: session.ObservedModel, Capabilities: session.Capabilities, ContextWindow: session.ContextWindow,
		}
	}
	return lv
}

// listData is the batch-read enrichment for the awake agents' rows (never nil maps).
type listData struct {
	displayNames, icons, nameColors, latestGoals map[string]string
	gitState                                     map[string]*store.GitSnapshot
	fileCounts                                   map[string]int
	latestEvents                                 map[string][2]string
	stateEvents                                  map[string][]store.AgentEvent
	tokenUsage                                   map[string]*store.TokenUsage
	latestTurnCtx                                map[string]int
	boardSubs                                    map[string]*board.Subscriber
	unread                                       map[string]int
}

// readListData runs the enrichment reads for agents. They don't depend on each other: they run concurrently, a few
// at a time, on the read pools (#3461). Run one after another they summed every connection wait, and under write
// load the live list's p95 reached ~1 s. Identities or subscriptions that can't be read are a listUnavailable; the
// rest are left empty.
func (h *SessionsHandler) readListData(ctx context.Context, trace *phaseTrace, agents []AgentInfo, lv liveIndex) (*listData, error) {
	sessionIDs := make([]string, 0, len(agents))
	sessionNames := make([]string, 0, len(agents))
	sessionAgents := make(map[string]string, len(agents))
	for _, a := range agents {
		if a.SessionID != "" && !lv.sleeping[a.SessionID] {
			sessionIDs = append(sessionIDs, a.SessionID)
			sessionNames = append(sessionNames, a.TmuxSession)
			sessionAgents[a.SessionID] = a.AgentName
		}
	}
	trace.begin("db_reads")
	var d listData
	var displayErr, subsErr, goalsErr, unreadErr error
	reads := []func(){
		func() { d.displayNames, displayErr = h.ss.GetDisplayNames(ctx, sessionIDs) },
		func() { d.icons, _ = h.ss.GetIcons(ctx, sessionIDs) },
		func() { d.nameColors, _ = h.ss.GetNameColors(ctx, sessionIDs) },
		func() { d.gitState, _ = h.gs.GetLiveGitState(ctx, sessionAgents) },
		func() { d.fileCounts, _ = h.ts.GetEditedFileCounts(ctx, sessionIDs) },
		func() { d.latestEvents, _ = h.ts.GetLatestEventTypes(ctx, sessionIDs) },
		func() { d.stateEvents, _ = h.ts.GetSessionStateEvents(ctx, sessionIDs) },
		func() { d.latestGoals, goalsErr = h.ts.GetLatestGoals(ctx, sessionIDs) },
	}
	if h.tokenStore != nil && len(sessionIDs) > 0 {
		reads = append(reads,
			func() { d.tokenUsage, _ = h.tokenStore.GetLatestUsageBySessionIDs(ctx, sessionIDs) },
			func() { d.latestTurnCtx, _ = h.tokenStore.GetLatestTurnContextBySessionIDs(ctx, sessionIDs) })
	}
	if h.bs != nil {
		reads = append(reads,
			func() { d.boardSubs, subsErr = h.bs.GetAllSubscriptions(ctx) },
			func() { d.unread, unreadErr = h.bs.GetUnreadCountsForSessions(ctx, sessionNames) })
	}
	runBounded(listReadConcurrency, reads)
	trace.endPool("db_reads", h.db, "reads", len(reads))
	if displayErr != nil {
		slog.Warn("failed to read live session identities", "error", displayErr)
		return nil, errListMetadata
	}
	if subsErr != nil {
		slog.Warn("failed to get board subscriptions", "error", subsErr)
		return nil, errListSubscriptions
	}
	if goalsErr != nil {
		slog.Warn("failed to get latest goals", "error", goalsErr)
	}
	if unreadErr != nil {
		slog.Warn("failed to get board unread counts", "error", unreadErr)
	}
	d.icons = orEmpty(d.icons)
	d.gitState = orEmpty(d.gitState)
	d.fileCounts = orEmpty(d.fileCounts)
	d.latestEvents = orEmpty(d.latestEvents)
	d.stateEvents = orEmpty(d.stateEvents)
	d.tokenUsage = orEmpty(d.tokenUsage)
	d.latestTurnCtx = orEmpty(d.latestTurnCtx)
	d.latestGoals = orEmpty(d.latestGoals)
	d.boardSubs = orEmpty(d.boardSubs)
	d.unread = orEmpty(d.unread)
	return &d, nil
}

// orEmpty is m, or an empty map for nil.
func orEmpty[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}

// agentRow is one awake agent's row: its log status (left to the background for a cold row of a large list), its
// state from events and transcript, git, board, goal, identity, model, tokens and context.
func (h *SessionsHandler) agentRow(trace *phaseTrace, agent AgentInfo, lv liveIndex, d *listData, autoAcceptStates map[string]store.AutoAcceptState, largeList bool) map[string]any {
	agentStarted := time.Now()
	var logInfo map[string]any
	sleeping := lv.sleeping[agent.SessionID]
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

	git := lookupGitState(d.gitState, sid, agent.AgentName)

	// Resolve changed file count (from Write/Edit tool events)
	fc := 0
	if sid != "" {
		if c, ok := d.fileCounts[sid]; ok {
			fc = c
		}
	}

	// Resolve latest event type for waiting/working detection
	var latestEv string
	if sid != "" {
		if ev, ok := d.latestEvents[sid]; ok {
			latestEv = ev[0]
		}
	}
	staleF, _ := staleness.(float64)
	notStarted := agentNeverStarted(agent.AgentType, latestEv, staleF, sessionAgeSeconds(lv.createdAt[sid]))
	stateInput := SessionStateInput{StalenessSeconds: staleF, NotStarted: notStarted, Sleeping: lv.sleeping[sid], Archived: lv.archived[sid]}
	for _, ev := range d.stateEvents[sid] {
		stateInput.Events = append(stateInput.Events, ev.StateEvent())
	}
	transcriptStarted := time.Now()
	if !skipExpensive {
		h.applyTranscriptState(&stateInput, d.stateEvents[sid], agent.AgentType, agent.TmuxSession, sid, agent.WorkingDir)
	}
	trace.agentDetail("transcript_state", sid, time.Since(transcriptStarted))
	state := DeriveSessionState(stateInput)

	// The goal line is the newest goal event (the goal generator or an
	// operator edit).
	summary := d.latestGoals[sid]

	// Board unread
	tmuxName := agent.TmuxSession
	boardSub := d.boardSubs[tmuxName]
	boardUnread := 0
	if boardSub != nil {
		boardUnread = d.unread[tmuxName]
	}

	branchVal, repoNameVal, worktreeVal := gitListFields(git)

	statusVal, summaryVal, displayVal, iconVal := listTextFields(logInfo, summary, d.displayNames, d.icons, sid)

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
		"created_at":            lv.createdAt[sid], // when this agent was started (a fixed time, so a row does not change between lists): the empty chat's "Waiting for the CLI to start" counts from it (#6058)
		"working_directory":     agent.WorkingDir,
		"display_name":          displayVal,
		"icon":                  iconVal,
		"name_color":            ptr.NullIfEmpty(d.nameColors[sid]),
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
		"live_permission_mode":  h.liveMode(sid),
		"working":               state.Working,
		"stuck":                 state.Stuck,
		"changed_file_count":    fc,
		"commands":              map[string]string{"compress": "/compact", "clear": "/clear"},
		"board_project":         boardProject(d.boardSubs, lv.boardNames, tmuxName, sid),
		"board_job_title":       boardJobTitle(d.boardSubs, lv.boardNames, tmuxName, sid),
		"board_unread":          boardUnread,
		"held_inputs":           ptymanager.HeldCount(tmuxName), // automated sends the gate holds (#3578)
		"board_is_orchestrator": boardSubscriberFlag(boardSub),
		"log_path":              agent.LogPath,
		"sleeping":              lv.sleeping[sid],
		"archived":              lv.archived[sid],
		"first_prompt":          firstPrompt,
		"enrichment_pending":    skipExpensive && !sleeping,
	}
	// Always present (false/""/null too): the dashboard merges entries, so
	// an omitted false would leave the AUTO chip on after it was turned off.
	for k, v := range autoAcceptFields(agent.AgentType, autoAcceptStates[sid]) {
		entry[k] = v
	}
	// Include prompt, model, and capabilities from live_sessions DB
	var observedModel *string
	if extra, ok := lv.extras[sid]; ok {
		observedModel = extra.Observed
		if extra.Prompt != nil {
			entry["prompt"] = *extra.Prompt
		}
		if extra.Model != nil && *extra.Model != "" {
			entry["model"] = *extra.Model
		}
		if extra.Thinking != nil && *extra.Thinking != "" {
			entry["thinking"] = *extra.Thinking // the level it was started with (#4969)
		}
		addCapabilities(entry, extra.Capabilities)
	}
	h.addTranscriptModel(entry, sid, observedModel)

	// Include token usage data if available
	if usage, ok := d.tokenUsage[sid]; ok {
		entry["token_input"] = usage.InputTokens
		entry["token_output"] = usage.OutputTokens
		entry["token_cache_read"] = usage.CacheReadTokens
		entry["token_cache_write"] = usage.CacheWriteTokens
		entry["token_cost_usd"] = usage.CostUSD
	}

	// Context window usage: latest turn's input_tokens / context_window.
	// Explicit nulls clear stale values in clients that spread-merge updates.
	var contextModel *string
	if extra, ok := lv.extras[sid]; ok {
		contextModel = extra.Model
	}
	addContextUsage(entry, contextModel, d.latestTurnCtx[sid])
	return entry
}

// sleepingRows are placeholder rows for sleeping sessions without an active terminal (not already in liveSIDs).
func (h *SessionsHandler) sleepingRows(allLive []store.LiveSession, liveSIDs map[string]bool) []map[string]any {
	var sessions []map[string]any
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
		entry := map[string]any{
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
		}
		// Include prompt, model, and capabilities for sleeping sessions too
		addContextUsage(entry, nil, 0)
		if ls.Prompt != nil {
			entry["prompt"] = *ls.Prompt
		}
		if ls.Model != nil && *ls.Model != "" {
			entry["model"] = *ls.Model
		}
		if ls.Thinking != nil && *ls.Thinking != "" {
			entry["thinking"] = *ls.Thinking
		}
		if o := ptr.Value(ls.ObservedModel); o != "" {
			entry["model"], entry["model_source"] = o, "hook"
		}
		addCapabilities(entry, ls.Capabilities)
		for k, v := range autoAcceptFields(ls.AgentType, store.AutoAcceptState{}) {
			entry[k] = v // asleep: always off
		}
		sessions = append(sessions, entry)
	}
	return sessions
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
	// An agent's token gets the roster fields of a session that is not its own or its board's (the live list's rule, #5041): not the working directory, the prompt or the waiting text.
	if owner, isAgent := VerifiedSession(r.Context()); isAgent && !h.agentMayReadSession(r.Context(), owner.SessionID, ls) {
		out = rosterRow(out)
	}
	writeJSON(w, http.StatusOK, out)
}

// agentMayReadSession reports whether the agent whose session is ownerSessionID may read ls whole: its own session, or one on its own board.
func (h *SessionsHandler) agentMayReadSession(ctx context.Context, ownerSessionID string, ls *store.LiveSession) bool {
	if ls.SessionID == ownerSessionID {
		return true
	}
	own, err := h.ss.GetLiveSession(ctx, ownerSessionID)
	if err != nil || own == nil {
		return false
	}
	board := ptr.Value(own.BoardName)
	return board != "" && board == ptr.Value(ls.BoardName)
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
	writeError( // (Callers check namesNoSession first: an exact "type-<uuid>" terminal name names one session.)
		w, http.StatusBadRequest, "session_id_required", "session_id is required")
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
		errNotFound(w, "no matching session")
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
			defer safego.Recover("server/routes/sessions_list:749")
			defer func() { <-sem; wg.Done() }()
			fn()
		}(fn)
	}
	wg.Wait()
}
