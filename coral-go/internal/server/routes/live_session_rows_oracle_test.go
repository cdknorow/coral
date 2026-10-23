package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// liveSessionRows, split into the live-session index, the batch reads, one awake agent's row and the sleeping
// placeholders (#4713), gives the rows liveSessionRowsBefore4713 (the function at v1.4.92, below) gives. Each seed
// is a board of agents (awake, asleep with or without a terminal, archived, stopped, unknown to the database) with
// events, identities, boards, unread messages, auto-accept and models; both run on it, in alternating order, and
// their rows are compared as JSON. Each kind of row has a floor, so none goes unexercised.
//
// The large-list path (more than 8 agents, not full) leaves cold rows to background warming, which finishes when it
// finishes: its rows depend on timing, so it is run full or with 8 agents or fewer here.

// orderedTerm lists its panes by name: the mock's map order would reorder the rows between two calls.
type orderedTerm struct{ *mockSessionTerminal }

func (o orderedTerm) ListSessions(ctx context.Context) ([]ptymanager.PaneInfo, error) {
	panes, err := o.mockSessionTerminal.ListSessions(ctx)
	sort.Slice(panes, func(i, j int) bool { return panes[i].SessionName < panes[j].SessionName })
	return panes, err
}

func lsrRows(t *testing.T, h *SessionsHandler, old, full bool) string {
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/live", nil)
	var rows []map[string]any
	var err error
	if old {
		rows, err = h.liveSessionRowsBefore4713(req, newPhaseTrace("oracle"), full)
	} else {
		rows, err = h.liveSessionRows(req, newPhaseTrace("oracle"), full)
		for _, r := range rows {
			delete(r, "team_health_on")       // new in #4962: the old implementation has no such field
			delete(r, "live_permission_mode") // new in #5951: the old implementation has no such field
			delete(r, "terminal")             // new in 159 (which terminal the agent runs on): the old implementation has no such field; live_terminal_field_test.go pins it through the route
		}
	}
	b, merr := json.Marshal(rows)
	require.NoError(t, merr)
	return fmt.Sprintf("err=%v rows=%s", err, b)
}

func TestLiveSessionRowsAgreesWithBefore4713(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(nopLog{}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	ctx := context.Background()
	kinds := map[string]int{}
	for seed := int64(1); seed <= 30; seed++ {
		rng := rand.New(rand.NewSource(seed))
		_, h, mock, ss := setupSessionsTestServer(t)
		h.terminal = orderedTerm{mock}
		n := 3 + rng.Intn(10)
		noRow := map[string]bool{}
		for i := 0; i < n; i++ {
			agentType := []string{"claude", "codex", "agy", "gemini"}[rng.Intn(4)]
			sid := fmt.Sprintf("%08x-4713-4000-8000-%012x", seed, i)
			name := naming.SessionName(agentType, sid)
			dir := fmt.Sprintf("/tmp/proj-%d", i)
			inTerminal := rng.Intn(6) > 0
			if inTerminal {
				mock.addSession(name, dir)
			}
			if rng.Intn(8) == 0 {
				noRow[sid] = true
				continue // a terminal Coral has no row for
			}
			ls := &store.LiveSession{SessionID: sid, AgentType: agentType, AgentName: fmt.Sprintf("agent-%d", i), WorkingDir: dir}
			if rng.Intn(2) == 0 {
				ls.BoardName = lsrStr([]string{"team", "other"}[rng.Intn(2)])
				ls.DisplayName = lsrStr([]string{"Lead Developer", "QA Engineer", "Orchestrator"}[rng.Intn(3)])
			}
			if rng.Intn(2) == 0 {
				ls.Prompt = lsrStr("build the thing")
			}
			if rng.Intn(2) == 0 {
				ls.Model = lsrStr([]string{"claude-opus-5-5", "", "gpt-6-astra"}[rng.Intn(3)])
			}
			if rng.Intn(3) == 0 {
				ls.Capabilities = lsrStr(`{"tools":true}`)
			}
			require.NoError(t, ss.RegisterLiveSession(ctx, ls))
			if rng.Intn(5) == 0 { // asleep (with or without its terminal)
				_, err := h.db.ExecContext(ctx, "UPDATE live_sessions SET is_sleeping = 1, sleep_reason = ? WHERE session_id = ?", []string{"idle", "operator"}[rng.Intn(2)], sid)
				require.NoError(t, err)
			}
			if rng.Intn(5) == 0 { // archived (asleep or not)
				_, err := h.db.ExecContext(ctx, "UPDATE live_sessions SET is_archived = 1 WHERE session_id = ?", sid)
				require.NoError(t, err)
			}
			if rng.Intn(4) == 0 { // a model its hooks reported
				_, err := h.db.ExecContext(ctx, "UPDATE live_sessions SET observed_model = ? WHERE session_id = ?", "claude-sonnet-5-5", sid)
				require.NoError(t, err)
			}
			if rng.Intn(3) == 0 {
				require.NoError(t, ss.SetDisplayName(ctx, sid, fmt.Sprintf("Nick %d", i)))
			}
			if rng.Intn(3) == 0 {
				require.NoError(t, ss.SetIcon(ctx, sid, lsrStr("🦀")))
			}
			if rng.Intn(3) == 0 {
				require.NoError(t, ss.SetNameColor(ctx, sid, lsrStr("#ff8800")))
			}
			if rng.Intn(4) == 0 {
				_, err := ss.EnableAutoAccept(ctx, sid, "test", 0)
				require.NoError(t, err)
			}
			if rng.Intn(3) == 0 { // launched minutes ago, its log quiet since (2.5 min: shown as 2 min for 30 s), no event yet
				logPath := naming.LogFile(h.cfg.LogDir, agentType, sid)
				require.NoError(t, os.WriteFile(logPath, []byte("starting up\n"), 0o600))
				old := time.Now().Add(-150 * time.Second)
				require.NoError(t, os.Chtimes(logPath, old, old))
				_, err := h.db.ExecContext(ctx, "UPDATE live_sessions SET created_at = ? WHERE session_id = ?", time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339), sid)
				require.NoError(t, err)
				continue
			}
			for k := rng.Intn(4); k > 0; k-- {
				typ := []string{"stop", "prompt_submit", "notification", "permission_request", "tool_use", "goal", "session_reset"}[rng.Intn(7)]
				_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: name, SessionID: &sid, EventType: typ, Summary: typ + " summary"})
				require.NoError(t, err)
			}
			if inTerminal && rng.Intn(2) == 0 {
				project := []string{"team", "other"}[rng.Intn(2)]
				_, err := h.bs.Subscribe(ctx, project, fmt.Sprintf("member-%d", i), []string{"Developer", "Orchestrator"}[rng.Intn(2)], name, nil, "all")
				require.NoError(t, err)
				if rng.Intn(2) == 0 {
					_, err := h.bs.PostMessage(ctx, project, "someone", "@all hello", nil)
					require.NoError(t, err)
				}
			}
		}
		for i := 0; i < 2; i++ {
			full := rng.Intn(2) == 0 || len(mock.sessions) > 8
			oldFirst := (seed+int64(i))%2 == 0
			var old, got string
			if oldFirst {
				old, got = lsrRows(t, h, true, full), lsrRows(t, h, false, full)
			} else {
				got, old = lsrRows(t, h, false, full), lsrRows(t, h, true, full)
			}
			require.Equal(t, old, got, "seed %d call %d (full %v)", seed, i, full)
			var rows []map[string]any
			require.NoError(t, json.Unmarshal([]byte(got[len("err=<nil> rows="):]), &rows))
			for _, r := range rows {
				for _, k := range lsrKinds(r) {
					kinds[k]++
				}
				if sid, _ := r["session_id"].(string); noRow[sid] {
					kinds["no row in the database"]++
				}
			}
		}
	}
	t.Logf("rows compared, per kind: %v", kinds)
	for _, k := range []string{"awake", "sleeping placeholder", "archived", "with a display name", "with an icon", "with a name colour",
		"on a board (subscribed)", "on a board (from its launch)", "unread on its board", "auto-accept on", "with a model", "model seen by a hook",
		"with a prompt", "waiting for input", "working", "not started", "with capabilities", "no row in the database",
		"asleep and archived", "asleep, model seen by a hook"} {
		assert.GreaterOrEqual(t, kinds[k], 5, "%q went (almost) unexercised", k)
	}
}

func lsrStr(s string) *string { return &s }

type nopLog struct{}

func (nopLog) Write(p []byte) (int, error) { return len(p), nil }

func lsrKinds(r map[string]any) []string {
	var out []string
	add := func(on bool, k string) {
		if on {
			out = append(out, k)
		}
	}
	sleeping, _ := r["sleeping"].(bool)
	add(!sleeping, "awake")
	add(sleeping && r["tmux_session"] == nil, "sleeping placeholder")
	add(r["archived"] == true, "archived")
	add(sleeping && r["archived"] == true, "asleep and archived")
	add(sleeping && r["model_source"] == "hook", "asleep, model seen by a hook")
	if dn, _ := r["display_name"].(string); dn != "" && len(dn) > 4 && dn[:4] == "Nick" {
		out = append(out, "with a display name")
	}
	add(r["icon"] != nil && r["icon"] != "", "with an icon")
	add(r["name_color"] != nil, "with a name colour")
	if bp, _ := r["board_project"].(string); bp != "" {
		if r["board_is_orchestrator"] != nil {
			out = append(out, "on a board (subscribed)")
		} else {
			out = append(out, "on a board (from its launch)")
		}
	}
	if u, _ := r["board_unread"].(float64); u > 0 {
		out = append(out, "unread on its board")
	}
	add(r["auto_accept"] == true, "auto-accept on")
	add(r["model"] != nil, "with a model")
	add(r["model_source"] == "hook", "model seen by a hook")
	add(r["prompt"] != nil, "with a prompt")
	add(r["waiting_for_input"] == true, "waiting for input")
	add(r["working"] == true, "working")
	add(r["not_started"] == true, "not started")
	add(r["capabilities"] != nil || r["tools"] != nil, "with capabilities")
	return out
}

// liveSessionRowsBefore4713 is liveSessionRows before #4713 split it (sessions_list.go at v1.4.92), kept to compare with.
func (h *SessionsHandler) liveSessionRowsBefore4713(r *http.Request, trace *phaseTrace, full bool) ([]map[string]any, error) {
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
		Thinking      *string
		Observed      *string
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
			Prompt: session.Prompt, Model: session.Model, Thinking: session.Thinking, Observed: session.ObservedModel, Capabilities: session.Capabilities, ContextWindow: session.ContextWindow,
		}
	}
	trace.endPool("db_live_session_extras", h.db)

	var sessions []map[string]any
	liveSIDs := make(map[string]bool) // track session IDs already in results
	// Keep the response path bounded for large teams: cold log enrichment is
	// warmed asynchronously and appears on the next sidebar refresh.
	largeList := !full && len(agents) > 8
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
			"starting":              agentStarting(agent.AgentType, latestEv, sessionAgeSeconds(createdAtMap[sid]), liveSleeping[sid], liveArchived[sid]),
			"created_at":            createdAtMap[sid], // new in #6058, here too: a fixed time
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
			"held_since":            heldSinceField(tmuxName),       // when the oldest of them was first held, UTC, null at none (#6503)
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
		var observedModel *string
		if extra, ok := liveExtras[sid]; ok {
			observedModel = extra.Observed
			if extra.Prompt != nil {
				entry["prompt"] = *extra.Prompt
			}
			if extra.Model != nil && *extra.Model != "" {
				entry["model"] = *extra.Model
			}
			if extra.Thinking != nil && *extra.Thinking != "" {
				entry["thinking"] = *extra.Thinking
			}
			addCapabilities(entry, extra.Capabilities)
		}
		h.addTranscriptModel(entry, sid, observedModel)

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
			"starting":              false,
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
	}

	h.addBoardLabels(r.Context(), sessions)
	return archivedLast(sessions), nil
}
