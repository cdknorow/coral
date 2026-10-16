// Package routes implements HTTP handlers for the Coral API.
package routes

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cdknorow/coral/internal/testguard"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/jsonl"
	"github.com/cdknorow/coral/internal/logparse"
	"github.com/cdknorow/coral/internal/machinehealth"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/safefs"
	"github.com/cdknorow/coral/internal/sessionstate"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
	"github.com/cdknorow/coral/internal/tracking"
)

// SessionsHandler handles all live session API endpoints.
type SessionsHandler struct {
	// inOldTmux reports an agent session still running in Coral's private tmux server, for a
	// pty Coral's wake (#3634). Tests replace it.
	inOldTmux func(ctx context.Context, sessionName string) bool
	// autoWake is the start's wake of the agents running when a pty Coral stopped (#3636).
	autoWake autoWaker
	// cliVersionOf finds a CLI's version (agent.CLIVersion; tests replace it), and
	// cliVersionTried the sessions a missing version was already looked up for (#3573).
	cliVersionOf      func(settings map[string]string, agentType string) string
	cliVersionTried   sync.Map
	roleRes           roleReservations                          // #3543: numbered names picked for launches not yet subscribed
	agentOperatorGate func(r *http.Request, action string) bool // #3536: Operator-only choices in a request body
	// wsHeartbeat overrides wsHeartbeatInterval when set (nanoseconds; tests).
	wsHeartbeat  atomic.Int64
	remoteAgents background.RemoteAgentSource
	db           *store.DB
	ss           *store.SessionStore
	ts           *store.TaskStore
	subagents    *store.SubagentStore
	gs           *store.GitStore
	bs           *board.Store
	cfg          *config.Config
	terminal     ptymanager.SessionTerminal
	jsonl        *jsonl.SessionReader
	backend      ptymanager.TerminalBackend // nil = use tmux directly

	boardHandler  *BoardHandler          // for sleep/wake board pausing
	schedStore    *store.ScheduleStore   // for active runs in websocket
	notifications *NotificationStore     // for UI notifications via websocket
	teamStore     *store.TeamStore       // for team persistence
	tokenStore    *store.TokenUsageStore // for token usage tracking

	goals GoalRequester // nil until the goal generator starts

	machineHealth MachineHealthSource // nil until StartMachineHealth (#3444)

	pending pendingTools // tool calls started but not finished (PreToolUse hook)

	autoAccept *AutoAcceptor // answers approval prompts for auto-accept sessions (auto_accept.go)

	wsList wsTickCache // one /ws/coral build shared by all clients (ws_shared_list.go)
	wsKick wsKicker    // prompt /ws/coral rebuilds after state changes (ws_kick.go)

	// captureCache keeps each session's last poll capture with the backend's output stamp
	// (#3439): while the pane has printed nothing, the poll reuses it instead of capture-pane.
	captureMu    sync.Mutex
	captureCache map[string]cachedCapture

	// logStatusCache avoids rereading each agent's bounded log tail on every
	// sidebar poll. Entries are invalidated by size/mtime and capped so a
	// burst of short-lived sessions cannot grow memory without bound.
	logStatusMu       sync.Mutex
	logStatusCache    map[string]cachedLogStatus
	logWarmMu         sync.Mutex
	logWarm           map[string]bool
	logWarmSem        chan struct{}
	transcriptWarmMu  sync.Mutex
	transcriptWarm    map[string]bool
	transcriptPending map[string]bool
	transcriptWarmSem chan struct{}
	// resumeCache avoids repeating the authoritative live-session lookup on
	// every one-second chat poll. Session IDs are immutable; the cached value
	// is still loaded from the database and is never taken from the client.
	resumeMu    sync.Mutex
	resumeCache map[string]string
	// chatMeta caches the live-session record a chat request needs (#3438).
	chatMeta    *chatMetaCache
	resumeKnown map[string]bool

	cachePrune sessionCachePruneState // session_cache_prune.go
}

type cachedLogStatus struct {
	info    os.FileInfo
	size    int64
	modTime time.Time
	value   map[string]any
}

// GoalRequester asks the goal generator for a fresh goal for one session.
type GoalRequester interface {
	RequestGoal(sessionID string)
}

// SetGoalRequester wires the goal generator used by the RequestGoal endpoint.
func (h *SessionsHandler) SetGoalRequester(g GoalRequester) {
	h.goals = g
}

// RequestGoal asks the goal generator to regenerate a session's goal line.
// The result arrives on the normal session updates.
// POST /api/sessions/live/{name}/goal  body: {"agent_type", "session_id"}
func (h *SessionsHandler) RequestGoal(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		AgentType string `json:"agent_type"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.SessionID == "" {
		errBadRequest(w, "session_id required")
		return
	}
	if _, status, message := h.validateExactLiveTarget(r.Context(), name, body.AgentType, body.SessionID, false); status != 0 {
		writeExactTargetError(w, status, message)
		return
	}
	if h.goals == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "goal generation is not running"})
		return
	}
	if settings, err := h.ss.GetSettings(r.Context()); err == nil && settings["auto_goals"] == "false" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "goal generation is turned off"})
		return
	}
	h.goals.RequestGoal(body.SessionID)
	writeJSON(w, http.StatusAccepted, map[string]any{"goal_pending": true})
}

// SetBoardHandler sets the board handler reference for sleep/wake operations.
func (h *SessionsHandler) SetBoardHandler(bh *BoardHandler) {
	h.boardHandler = bh
}

// MachineHealthSource is the latest machine health sample (#3444); machinehealth.Sampler.
type MachineHealthSource interface {
	Current() (machinehealth.Health, bool)
}

// SetMachineHealth gives the feeds (/ws/coral, the dashboard's and a hub's) the machine's health.
func (h *SessionsHandler) SetMachineHealth(src MachineHealthSource) {
	h.machineHealth = src
}

// SetNotificationStore sets the notification store for WebSocket delivery.
func (h *SessionsHandler) SetNotificationStore(ns *NotificationStore) {
	h.notifications = ns
}

// SetScheduleStore sets the schedule store for active runs in websocket.
func (h *SessionsHandler) SetScheduleStore(ss *store.ScheduleStore) {
	h.schedStore = ss
}

// SetAgentOperatorGate sets the check for Operator-only choices an agent's token may not
// make (IdentityObserver.AgentOperatorAction): true means refuse.
func (h *SessionsHandler) SetAgentOperatorGate(fn func(r *http.Request, action string) bool) {
	h.agentOperatorGate = fn
}

// SetTeamStore sets the team store for team persistence.
func (h *SessionsHandler) SetTeamStore(ts *store.TeamStore) {
	h.teamStore = ts
}

// SetTokenStore sets the token usage store for tracking.
func (h *SessionsHandler) SetTokenStore(ts *store.TokenUsageStore) {
	h.tokenStore = ts
}

// getDiffMode reads the git_diff_mode from global user settings.
// Returns "" (default branch_point), "previous_commit", or "main_head".
func (h *SessionsHandler) getDiffMode(ctx context.Context) string {
	settings, err := h.ss.GetSettings(ctx)
	if err != nil {
		return ""
	}
	return settings["git_diff_mode"]
}

// resolveModel returns the effective model for a launch. A non-empty
// requestModel always wins (after trimming whitespace). Otherwise it falls
// back to the user's default_model_<agentType> setting. Returns "" if
// neither is set. Terminal agents never receive a model.
func (h *SessionsHandler) resolveModel(ctx context.Context, agentType, requestModel string) string {
	if agentType == at.Terminal {
		return ""
	}
	if m := strings.TrimSpace(requestModel); m != "" {
		return m
	}
	if agentType == "" {
		return ""
	}
	settings, err := h.ss.GetSettings(ctx)
	if err != nil {
		return ""
	}
	return defaultModelFromSettings(settings, agentType, requestModel)
}

// defaultModelFromSettings looks up default_model_<agentType> in an already-loaded
// settings map. Used by LaunchTeam to avoid re-reading settings per agent.
// Terminal agents never receive a model.
func defaultModelFromSettings(settings map[string]string, agentType, requestModel string) string {
	if agentType == at.Terminal {
		return ""
	}
	if m := strings.TrimSpace(requestModel); m != "" {
		return m
	}
	if agentType == "" || settings == nil {
		return ""
	}
	model := strings.TrimSpace(settings["default_model_"+agentType])
	if model == "" {
		if agentType == at.Agy || agentType == at.Antigravity {
			model = strings.TrimSpace(settings["default_model_"+at.Gemini])
		} else if agentType == at.Gemini {
			model = strings.TrimSpace(settings["default_model_"+at.Agy])
		}
	}
	return model
}

// NewSessionsHandler creates a SessionsHandler with the given dependencies.
func NewSessionsHandler(db *store.DB, cfg *config.Config, backend ptymanager.TerminalBackend, terminal ptymanager.SessionTerminal, bs *board.Store) *SessionsHandler {
	return &SessionsHandler{
		autoWake: autoWaker{sleep: defaultSleep, now: time.Now},
		inOldTmux: func(ctx context.Context, name string) bool {
			return tmux.HasPrivateAgentSession(ctx, tmux.PrivateSocketPath(cfg.CoralDir()), name)
		},
		db:                db,
		ss:                store.NewSessionStore(db),
		ts:                store.NewTaskStore(db),
		subagents:         store.NewSubagentStore(db),
		gs:                store.NewGitStore(db),
		bs:                bs,
		cfg:               cfg,
		terminal:          terminal,
		jsonl:             jsonl.NewSessionReader(),
		backend:           backend,
		logStatusCache:    make(map[string]cachedLogStatus),
		logWarm:           make(map[string]bool),
		logWarmSem:        make(chan struct{}, 4),
		transcriptWarm:    make(map[string]bool),
		transcriptPending: make(map[string]bool),
		transcriptWarmSem: make(chan struct{}, 4),
		resumeCache:       make(map[string]string),
		chatMeta:          newChatMetaCache(),
		resumeKnown:       make(map[string]bool),
	}
}

func cloneLogStatus(value map[string]any) map[string]any {
	clone := make(map[string]any, len(value))
	for key, item := range value {
		if lines, ok := item.([]string); ok {
			clone[key] = append([]string(nil), lines...)
		} else {
			clone[key] = item
		}
	}
	return clone
}

func (h *SessionsHandler) cachedLogStatus(path string) map[string]any {
	if path == "" {
		return getLogStatus(path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return getLogStatus(path)
	}
	h.logStatusMu.Lock()
	if cached, ok := h.logStatusCache[path]; ok && cached.info != nil && os.SameFile(cached.info, info) && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		value := cloneLogStatus(cached.value)
		value["staleness_seconds"] = time.Since(info.ModTime()).Seconds()
		h.logStatusMu.Unlock()
		return value
	}
	h.logStatusMu.Unlock()
	value := getLogStatus(path)
	h.logStatusMu.Lock()
	if len(h.logStatusCache) >= 512 {
		for key := range h.logStatusCache {
			delete(h.logStatusCache, key)
			break
		}
	}
	h.logStatusCache[path] = cachedLogStatus{info: info, size: info.Size(), modTime: info.ModTime(), value: cloneLogStatus(value)}
	h.logStatusMu.Unlock()
	return value
}

func (h *SessionsHandler) readyLogStatus(path string) (map[string]any, bool) {
	if path == "" {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	h.logStatusMu.Lock()
	defer h.logStatusMu.Unlock()
	cached, ok := h.logStatusCache[path]
	if !ok || cached.info == nil || !os.SameFile(cached.info, info) || cached.size != info.Size() || !cached.modTime.Equal(info.ModTime()) {
		return nil, false
	}
	value := cloneLogStatus(cached.value)
	value["staleness_seconds"] = time.Since(info.ModTime()).Seconds()
	return value, true
}

func (h *SessionsHandler) warmLogStatus(path string) {
	if path == "" {
		return
	}
	h.logWarmMu.Lock()
	if h.logWarm[path] {
		h.logWarmMu.Unlock()
		return
	}
	h.logWarm[path] = true
	h.logWarmMu.Unlock()
	go func() {
		defer func() {
			h.logWarmMu.Lock()
			delete(h.logWarm, path)
			h.logWarmMu.Unlock()
		}()
		h.logWarmSem <- struct{}{}
		_ = h.cachedLogStatus(path)
		<-h.logWarmSem
	}()
}

func (h *SessionsHandler) warmTranscript(agent AgentInfo) {
	if agent.SessionID == "" {
		return
	}
	h.transcriptWarmMu.Lock()
	if h.transcriptWarm[agent.SessionID] || h.transcriptPending[agent.SessionID] {
		h.transcriptWarmMu.Unlock()
		return
	}
	h.transcriptPending[agent.SessionID] = true
	h.transcriptWarmMu.Unlock()
	go func() {
		defer func() {
			h.transcriptWarmMu.Lock()
			delete(h.transcriptPending, agent.SessionID)
			h.transcriptWarm[agent.SessionID] = true
			h.transcriptWarmMu.Unlock()
		}()
		h.transcriptWarmSem <- struct{}{}
		switch agent.AgentType {
		case at.Codex:
			h.jsonl.ReadCodexTurnEvent(agent.SessionID, agent.WorkingDir)
		case at.Agy, at.Antigravity, at.Gemini:
			h.jsonl.ReadAgyTurnEvent(agent.SessionID, agent.WorkingDir)
		}
		h.jsonl.FirstUserPrompt(agent.SessionID, agent.WorkingDir, agent.AgentType)
		<-h.transcriptWarmSem
	}()
}

func (h *SessionsHandler) transcriptReady(sessionID string) bool {
	h.transcriptWarmMu.Lock()
	defer h.transcriptWarmMu.Unlock()
	return h.transcriptWarm[sessionID]
}

// ── Agent Discovery ─────────────────────────────────────────────────────

// AgentInfo represents a discovered live agent.
type AgentInfo struct {
	AgentType   string `json:"agent_type"`
	AgentName   string `json:"agent_name"`
	SessionID   string `json:"session_id"`
	TmuxSession string `json:"tmux_session"`
	LogPath     string `json:"log_path"`
	WorkingDir  string `json:"working_directory"`
}

func (h *SessionsHandler) discoverAgents(ctx *http.Request) ([]AgentInfo, error) {
	h.maybePruneSessionCaches() // every list path discovers agents first
	panes, err := h.terminal.ListSessions(ctx.Context())
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var agents []AgentInfo

	for _, pane := range panes {
		agentType, sessionID := logparse.ParseSessionName(pane.SessionName)
		if agentType == "" || sessionID == "" {
			continue
		}
		if seen[sessionID] {
			continue
		}
		seen[sessionID] = true

		agentName := filepath.Base(strings.TrimRight(pane.CurrentPath, "/"))
		if agentName == "" {
			agentName = sessionID[:8]
		}

		logPath := naming.LogFile(h.cfg.LogDir, agentType, sessionID)

		agents = append(agents, AgentInfo{
			AgentType:   agentType,
			AgentName:   agentName,
			SessionID:   sessionID,
			TmuxSession: pane.SessionName,
			LogPath:     logPath,
			WorkingDir:  pane.CurrentPath,
		})
	}

	return h.dropStoppedAgents(ctx.Context(), agents), nil
}

// dropStoppedAgents removes runtime sessions that Coral has already marked as
// stopped in the DB. Such a session is an orphan (a kill that missed, or a
// process that outlived a server restart) and must not appear in the sidebar
// as if it were a live team member. The session reconciler cleans them up.
func (h *SessionsHandler) dropStoppedAgents(ctx context.Context, agents []AgentInfo) []AgentInfo {
	if h.ss == nil || len(agents) == 0 {
		return agents
	}
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.SessionID)
	}
	stopped, err := h.ss.GetStoppedSessions(ctx, ids)
	if err != nil || len(stopped) == 0 {
		return agents
	}
	kept := make([]AgentInfo, 0, len(agents))
	for _, a := range agents {
		if _, isStopped := stopped[a.SessionID]; isStopped {
			slog.Debug("hiding orphaned runtime session marked stopped in DB",
				"session", a.TmuxSession)
			continue
		}
		kept = append(kept, a)
	}
	return kept
}

// getLogStatus reads a log file tail and reports its staleness and recent
// lines. "status" and "summary" stay in the result, always nil, so clients that
// read them keep working; live state comes from provider session events and
// goals from goal events.
func getLogStatus(logPath string) map[string]any {
	result := map[string]any{
		"status":            nil,
		"summary":           nil,
		"staleness_seconds": nil,
		"recent_lines":      []string{},
	}

	info, err := os.Stat(logPath)
	if err != nil {
		return result
	}

	staleness := time.Since(info.ModTime()).Seconds()
	result["staleness_seconds"] = staleness

	// Read tail of the file (last ~256KB)
	const tailBytes = 256_000
	fileSize := info.Size()
	start := int64(0)
	if fileSize > tailBytes {
		start = fileSize - tailBytes
	}

	f, err := os.Open(logPath)
	if err != nil {
		return result
	}
	defer f.Close()

	if start > 0 {
		f.Seek(start, 0)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return result
	}

	// Split into lines, decode, strip ANSI
	rawLines := strings.Split(string(raw), "\n")
	if start > 0 && len(rawLines) > 0 {
		rawLines = rawLines[1:] // drop partial first line
	}

	cleanLines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		cleanLines = append(cleanLines, logparse.StripANSI(line))
	}

	result["recent_lines"] = logparse.RecentLines(cleanLines)

	return result
}

func usageLimitNotice(logPath string) string {
	return usageLimitNoticeFromStatus(getLogStatus(logPath))
}

func usageLimitNoticeFromStatus(info map[string]any) string {
	lines, _ := info["recent_lines"].([]string)
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "usage limit") || strings.Contains(lower, "weekly limit") {
			return "This agent reached its weekly usage limit and cannot continue until the limit resets."
		}
	}
	return ""
}

// cachedResumeFromID performs the authoritative lookup once per live session
// ID. The client never supplies or overrides this value; it only supplies the
// session ID used to select the cache entry. Session IDs are unique and a
// replacement session receives a new ID, so retaining an empty value is safe.
func (h *SessionsHandler) cachedResumeFromID(ctx context.Context, sessionID string) string {
	if sessionID == "" || h.ss == nil {
		return ""
	}
	h.resumeMu.Lock()
	if h.resumeKnown[sessionID] {
		value := h.resumeCache[sessionID]
		h.resumeMu.Unlock()
		return value
	}
	h.resumeMu.Unlock()

	ls, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil || ls == nil {
		return ""
	}
	value := derefStrPtr(ls.ResumeFromID)
	h.rememberResumeFromID(sessionID, value)
	return value
}

func (h *SessionsHandler) rememberResumeFromID(sessionID, value string) {
	if sessionID == "" {
		return
	}
	h.resumeMu.Lock()
	defer h.resumeMu.Unlock()
	if len(h.resumeKnown) >= 1024 && !h.resumeKnown[sessionID] {
		for key := range h.resumeKnown {
			delete(h.resumeKnown, key)
			delete(h.resumeCache, key)
			break
		}
	}
	h.resumeKnown[sessionID] = true
	h.resumeCache[sessionID] = value
}

// ── List / Detail ───────────────────────────────────────────────────────

// Sleeping sessions use existing display metadata without transcript discovery.
func (h *SessionsHandler) sleepingFirstPrompt(session store.LiveSession) string {
	if prompt := h.jsonl.CachedFirstUserPrompt(session.SessionID); prompt != "" {
		return prompt
	}
	return derefStrPtr(session.Prompt)
}

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
			liveBoardNames[session.SessionID] = [2]string{*session.BoardName, derefStrPtr(session.DisplayName)}
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
			h.applyTranscriptState(&stateInput, stateEvents[sid], agent.AgentType, sid, agent.WorkingDir)
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
			"name_color":            nilIfEmpty(nameColors[sid]),
			"branch":                branchVal,
			"repo_name":             repoNameVal,
			"git_state":             worktreeVal,
			"waiting_for_input":     state.NeedsInput,
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
		"board_label":       h.boardLabel(derefStrPtr(ls.BoardName)),
		"board_job_title":   ls.DisplayName,
		"icon":              ls.Icon,
		"name_color":        ls.NameColor,
		"status":            nilIfEmpty(initialStatus),
		"summary":           nilIfEmpty(summary),
		"first_prompt":      firstPrompt,
		"waiting_for_input": liveState.NeedsInput,
		"awaiting_user":     liveState.AwaitingUser,
		"waiting_reason":    nilIfEmpty(liveState.WaitingReason),
		"waiting_summary":   nilIfEmpty(liveState.WaitingSummary),
		"waiting_kind":      nilIfEmpty(liveState.WaitingKind),
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

// Detail returns detailed info for a specific live session.
// GET /api/sessions/live/{name}
func (h *SessionsHandler) Detail(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) {
		writeSessionIDRequired(w)
		return
	}
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, false); status != 0 {
			writeExactTargetError(w, status, message)
			return
		}
	}

	logPath := h.findLogPath(agentType, sessionID)
	logInfo := getLogStatus(logPath)

	paneText, _ := h.terminal.CaptureOutput(r.Context(), name, 200, agentType, sessionID)

	out := map[string]any{
		"name":              name,
		"session_id":        sessionID,
		"status":            logInfo["status"],
		"summary":           logInfo["summary"],
		"recent_lines":      logInfo["recent_lines"],
		"staleness_seconds": logInfo["staleness_seconds"],
		"pane_capture":      paneText,
	}
	if sessionID != "" {
		for k, v := range h.autoAcceptPayload(r.Context(), sessionID) {
			out[k] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Capture returns a tmux pane capture for a session.
// GET /api/sessions/live/{name}/capture
func (h *SessionsHandler) Capture(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) {
		writeSessionIDRequired(w)
		return
	}
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, false); status != 0 {
			writeExactTargetError(w, status, message)
			return
		}
	}

	text, err := h.terminal.CaptureOutput(r.Context(), name, 200, agentType, sessionID)
	if err != nil || text == "" {
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "capture": nil, "error": "Could not capture pane"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "capture": text})
}

// Poll returns capture, tasks, and events in a single batch response.
// GET /api/sessions/live/{name}/poll
func (h *SessionsHandler) Poll(w http.ResponseWriter, r *http.Request) {
	// With CORAL_DEBUG_PHASES=1, each poll logs its phases like the live list and
	// chat (coral-hub's tunnel measurements).
	trace := newPhaseTrace("live-poll")
	defer trace.finish()
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) {
		writeSessionIDRequired(w)
		return
	}
	trace.begin("target_check")
	if shouldValidateExactTarget(agentType, sessionID) {
		if _, status, message := h.validateExactLiveTarget(r.Context(), name, agentType, sessionID, false); status != 0 {
			writeExactTargetError(w, status, message)
			return
		}
	}
	trace.end("target_check")
	eventsLimit := clampLimit(queryInt(r, "events_limit", 50), 50, 200)

	ctx := r.Context()

	// Capture pane
	trace.begin("capture")
	captureResult := map[string]any{"name": name, "capture": nil}
	if text, err := h.captureForPoll(ctx, name, agentType, sessionID); err == nil && text != "" {
		captureResult["capture"] = text
	} else {
		captureResult["error"] = fmt.Sprintf("Could not capture pane for '%s'", name)
	}
	trace.end("capture")

	// Tasks
	trace.begin("tasks")
	var tasks any = []any{}
	if sessionID != "" {
		if t, err := h.ts.ListAgentTasks(ctx, name, &sessionID); err == nil {
			tasks = t
		}
	}
	trace.endPool("tasks", h.db)

	// Events
	trace.begin("events")
	var events any = []any{}
	if e, err := h.ts.ListAgentEvents(ctx, name, eventsLimit, strPtr(sessionID)); err == nil {
		events = e
	}
	trace.endPool("events", h.db)

	// Polled every 500 ms by an open terminal: an unchanged answer is a 304 with no body (#3439).
	httputil.WriteJSONValidated(w, r, http.StatusOK, map[string]any{
		"capture": captureResult,
		"tasks":   tasks,
		"events":  events,
	})
}

type cachedCapture struct {
	stamp string
	text  string
	at    time.Time
}

// captureReuseMax bounds how long a capture is reused on an unchanged output stamp: a few screen
// changes print nothing to the pane's output log (a resize reflowing the screen, tmux
// clear-history, copy-mode if anything ever uses it), and show at most this late.
const captureReuseMax = 5 * time.Second

// captureCacheMax caps the cache; it starts over when full (a burst of short-lived sessions).
const captureCacheMax = 256

// captureForPoll is CaptureOutput for the poll: when the backend can stamp the session's output
// and nothing was written since the last capture (and that is recent), the last capture is
// reused. The stamp is taken before capturing, so output printed meanwhile is seen next time.
func (h *SessionsHandler) captureForPoll(ctx context.Context, name, agentType, sessionID string) (string, error) {
	stamper, _ := h.backend.(ptymanager.OutputStamper)
	if stamper == nil {
		return h.terminal.CaptureOutput(ctx, name, 200, agentType, sessionID)
	}
	stamp, ok := stamper.OutputStamp(agentType, sessionID)
	key := name + "\x00" + agentType + "\x00" + sessionID
	if ok {
		h.captureMu.Lock()
		c, hit := h.captureCache[key]
		h.captureMu.Unlock()
		if hit && c.stamp == stamp && time.Since(c.at) < captureReuseMax {
			return c.text, nil
		}
	}
	text, err := h.terminal.CaptureOutput(ctx, name, 200, agentType, sessionID)
	h.captureMu.Lock()
	defer h.captureMu.Unlock()
	if ok && err == nil && text != "" {
		if h.captureCache == nil || len(h.captureCache) >= captureCacheMax {
			h.captureCache = map[string]cachedCapture{}
		}
		h.captureCache[key] = cachedCapture{stamp: stamp, text: text, at: time.Now()}
	} else {
		delete(h.captureCache, key)
	}
	return text, err
}

// Chat returns the JSONL conversation transcript.
// GET /api/sessions/live/{name}/chat
func (h *SessionsHandler) Chat(w http.ResponseWriter, r *http.Request) {
	trace := newPhaseTrace("live-chat")
	defer trace.finish()
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	workingDir := r.URL.Query().Get("working_directory")
	var resumeFromID string
	var resumeLookupDone bool
	metaCache := "not_needed" // hit: from memory; miss: read from the sessions DB (#3438)
	trace.begin("metadata_lookup")

	// If missing metadata, attempt lookup by session_id or name: from memory when it was
	// read recently (#3438: a chat through Coral Hub always lacks the working directory,
	// and the sessions DB has one connection shared with every background writer).
	if agentType == "" || workingDir == "" || sessionID == "" {
		targetID := sessionID
		if targetID == "" {
			targetID = name
		}
		metaCache = "miss"
		if m, ok := h.chatMeta.get(targetID); ok {
			metaCache = "hit"
			resumeLookupDone = true
			resumeFromID = m.resumeFromID
			if agentType == "" {
				agentType = m.agentType
			}
			if workingDir == "" {
				workingDir = m.workingDir
			}
			if sessionID == "" {
				sessionID = m.sessionID
			}
		}
	}
	if (agentType == "" || workingDir == "" || sessionID == "") && h.ss != nil && !resumeLookupDone {
		targetID := sessionID
		if targetID == "" {
			targetID = name
		}
		if ls, err := h.ss.GetLiveSession(r.Context(), targetID); err == nil && ls != nil {
			h.chatMeta.put(chatMeta{agentType: ls.AgentType, workingDir: ls.WorkingDir, sessionID: ls.SessionID, resumeFromID: derefStrPtr(ls.ResumeFromID)}, targetID, ls.SessionID)
			resumeLookupDone = true
			resumeFromID = derefStrPtr(ls.ResumeFromID)
			h.rememberResumeFromID(ls.SessionID, resumeFromID)
			if agentType == "" && ls.AgentType != "" {
				agentType = ls.AgentType
			}
			if workingDir == "" && ls.WorkingDir != "" {
				workingDir = ls.WorkingDir
			}
			if sessionID == "" {
				sessionID = ls.SessionID
			}
		} else if all, err := h.ss.GetAllLiveSessions(r.Context()); err == nil {
			matches := 0
			for _, s := range all {
				if s.AgentName == name || s.SessionID == name {
					matches++
				}
			}
			for _, s := range all {
				if s.AgentName == name || s.SessionID == name {
					// Cached under the name only when it names one session: an agent name is a
					// folder name, which several agents can share (the first match is used, as
					// before, but not pinned).
					keys := []string{s.SessionID}
					if matches == 1 {
						keys = append(keys, name)
					}
					h.chatMeta.put(chatMeta{agentType: s.AgentType, workingDir: s.WorkingDir, sessionID: s.SessionID, resumeFromID: derefStrPtr(s.ResumeFromID)}, keys...)
					resumeLookupDone = true
					resumeFromID = derefStrPtr(s.ResumeFromID)
					h.rememberResumeFromID(s.SessionID, resumeFromID)
					if agentType == "" && s.AgentType != "" {
						agentType = s.AgentType
					}
					if workingDir == "" && s.WorkingDir != "" {
						workingDir = s.WorkingDir
					}
					if sessionID == "" {
						sessionID = s.SessionID
					}
					break
				}
			}
		}
	}
	// Live chat clients normally provide all session metadata explicitly. Even
	// in that case, load the persisted resume lineage so a replacement session
	// can read its trusted ancestor rollout until the new marker is emitted.
	if !resumeLookupDone && sessionID != "" {
		resumeFromID = h.cachedResumeFromID(r.Context(), sessionID)
	}
	trace.endPool("metadata_lookup", h.db, "meta_cache", metaCache)

	if agentType == "" {
		agentType = at.Claude
	}

	// Use session_id if provided, otherwise use name as session_id
	id := sessionID
	if id == "" {
		id = name
	}

	// When after=0, the client wants the full conversation history (e.g. after
	// switching to the Chat tab). Return all accumulated messages, not just new ones.
	// Supports pagination: ?after=0&limit=100&offset=0 returns the last 100 messages.
	// Otherwise return messages[after:]. The reader's cache is shared by every
	// client, so "new since the last read" would split new messages between
	// windows polling the same session; each client's own count is used instead.
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	// Live requests carry an exact session ID. Antigravity's native
	// conversation IDs can also be UUIDs, so strict marker validation belongs
	// only on this explicit live-session path, not generic history browsing.
	var messages []map[string]any
	var total int
	trace.begin("transcript_read")
	if sessionID != "" {
		if resumeFromID != "" {
			messages, total = h.jsonl.ReadAllMessagesForLiveWithLineage(id, workingDir, agentType, resumeFromID)
		} else {
			messages, total = h.jsonl.ReadAllMessagesForLive(id, workingDir, agentType)
		}
	} else {
		messages, total = h.jsonl.ReadAllMessages(id, workingDir, agentType)
	}
	trace.end("transcript_read", "messages", total)
	trace.begin("log_status")
	// Reuse the bounded log-status cache used by the live-session list. Chat
	// polls run every second; reparsing the same 256KB log tail on each poll
	// adds avoidable filesystem and parsing work when the log is unchanged.
	limitNotice := usageLimitNoticeFromStatus(h.cachedLogStatus(h.findLogPath(agentType, sessionID)))
	trace.end("log_status")
	if after > 0 {
		messages = messages[min(after, len(messages)):]
	}
	messages = emptyIfNil(messages)

	// Pagination for full history: return the `limit` messages before a
	// cursor. `before` is an absolute message index (exclusive), so an older
	// page stays put while new messages are appended; pass the previous page's
	// `start` to get the page above it. `offset` counts backwards from the
	// current end and is kept for older clients, but it shifts whenever a
	// message arrives between two pages. `start` is the absolute index of the
	// first message returned. An empty transcript is a page too (start 0), and
	// a bad before is refused whether or not there are messages.
	if after == 0 {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 100 // default page size
		}
		end := len(messages)
		if raw := r.URL.Query().Get("before"); raw != "" {
			before, err := strconv.Atoi(raw)
			if err != nil || before < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "before must be a non-negative integer"})
				return
			}
			end = min(before, end)
		} else {
			// offset=0 means the most recent messages; offset=100 means skip the last 100
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			end = max(end-max(offset, 0), 0)
		}
		start := max(end-limit, 0)
		httputil.WriteJSONCompressed(w, r, http.StatusOK, map[string]any{
			"messages":     messages[start:end],
			"total":        total,
			"start":        start,
			"has_more":     start > 0,
			"limit_notice": limitNotice,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"messages": messages, "total": total, "limit_notice": limitNotice})
}

// Info returns enriched metadata for the session info modal.
// GET /api/sessions/live/{name}/info
func (h *SessionsHandler) Info(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	agentType := r.URL.Query().Get("agent_type")
	sessionID := r.URL.Query().Get("session_id")
	if namesNoSession(name, sessionID) {
		writeSessionIDRequired(w)
		return
	}
	ctx := r.Context()

	pane, _ := h.terminal.FindSession(ctx, name, agentType, sessionID)

	result := map[string]any{
		"name":       name,
		"session_id": sessionID,
		"agent_name": name,
		"agent_type": agentType,
	}

	if pane != nil {
		result["tmux_session_name"] = pane.SessionName
		result["pane_title"] = pane.PaneTitle
		result["working_directory"] = pane.CurrentPath
		result["tmux_command"] = h.terminal.AttachCommand(pane.SessionName)
	}

	// Include log path and other metadata from live session record
	if sessionID != "" {
		if ls, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && ls != nil {
			if ls.DisplayName != nil && *ls.DisplayName != "" {
				result["agent_name"] = *ls.DisplayName
			}
			result["agent_type"] = ls.AgentType
			// Construct log path from agent type + session ID
			logPath := naming.LogFile(h.cfg.LogDir, ls.AgentType, sessionID)
			result["log_path"] = logPath
		}
	}

	// Look up git state by session_id first, then by name
	var git *store.GitSnapshot
	if sessionID != "" {
		var gitErr error
		git, gitErr = h.gs.GetLatestGitStateBySession(ctx, sessionID)
		if gitErr != nil {
			slog.Warn("failed to get git state by session", "session_id", sessionID, "error", gitErr)
		}
	}
	if git == nil {
		var gitErr error
		git, gitErr = h.gs.GetLatestGitState(ctx, name)
		if gitErr != nil {
			slog.Warn("failed to get git state by name", "agent_name", name, "error", gitErr)
		}
	}
	if git != nil {
		result["git_branch"] = git.Branch
		result["git_commit_hash"] = git.CommitHash
		result["git_commit_subject"] = git.CommitSubject
		if git.RemoteURL != nil {
			if rn := gitutil.ParseRepoName(*git.RemoteURL); rn != "" {
				result["git_repo"] = rn
			}
		}
	}

	// Include prompt and board info from live session record
	if sessionID != "" {
		if info, err := h.ss.GetLiveSessionPromptInfo(ctx, sessionID); err == nil && info != nil {
			if info.Prompt != nil {
				result["prompt"] = *info.Prompt
			}
			if info.BoardName != nil {
				result["board_name"] = *info.BoardName
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// ── Files / Git ─────────────────────────────────────────────────────────

// SessionStatus returns lifecycle and agent-turn state for one stable session ID.
// GET /api/sessions/{sessionID}/status
func (h *SessionsHandler) SessionStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" || strings.ContainsAny(sessionID, "/\\\x00") {
		errBadRequest(w, "invalid session_id")
		return
	}

	var row struct {
		Status     string  `db:"status"`
		IsSleeping int     `db:"is_sleeping"`
		StoppedAt  *string `db:"stopped_at"`
	}
	err := h.db.Reader().GetContext(r.Context(), &row,
		"SELECT status, is_sleeping, stopped_at FROM live_sessions WHERE session_id = ?", sessionID)
	if err == sql.ErrNoRows {
		var indexed bool
		if indexErr := h.db.Reader().GetContext(r.Context(), &indexed,
			"SELECT EXISTS(SELECT 1 FROM session_index WHERE session_id = ?)", sessionID); indexErr != nil || !indexed {
			errNotFound(w, "session not found")
			return
		}
		row.Status = "inactive"
	} else if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	finished := row.Status != "active"
	sleeping := !finished && row.IsSleeping == 1
	latestEvent, latestSummary := "", ""
	if events, eventErr := h.ts.GetLatestEventTypes(r.Context(), []string{sessionID}); eventErr == nil {
		latestEvent = events[sessionID][0]
		latestSummary = events[sessionID][1]
	}
	// Shared derivation: only a request made directly to the user is
	// "waiting_for_input". A finished turn, including the idle reminder that
	// follows it, is "done" here, which is this endpoint's name for the
	// list row's awaiting_user.
	var liveState SessionState
	if !finished && !sleeping {
		liveState = h.deriveSingleSessionState(r.Context(), sessionID, 0)
	}
	waiting := liveState.NeedsInput
	done := liveState.AwaitingUser

	state := "active"
	switch {
	case finished:
		state = "finished"
	case sleeping:
		state = "sleeping"
	case waiting:
		state = "waiting_for_input"
	case done:
		state = "done"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":        sessionID,
		"state":             state,
		"active":            !finished,
		"finished":          finished,
		"waiting_for_input": waiting,
		"awaiting_user":     done,
		"waiting_reason":    nilIfEmpty(liveState.WaitingReason),
		"waiting_summary":   nilIfEmpty(liveState.WaitingSummary),
		"waiting_kind":      nilIfEmpty(liveState.WaitingKind),
		"done":              done,
		"sleeping":          sleeping,
		"latest_event":      nilIfEmpty(latestEvent),
		"latest_summary":    nilIfEmpty(latestSummary),
		"stopped_at":        row.StoppedAt,
	})
}

// resolveWorkdir determines the working directory for an agent session.
func (h *SessionsHandler) resolveWorkdir(ctx context.Context, name, agentType, sessionID string) string {
	pane, _ := h.terminal.FindSession(ctx, name, agentType, sessionID)
	if pane != nil {
		if pane.CurrentPath != "" {
			return pane.CurrentPath
		}
	}
	if sessionID != "" {
		if live, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && live != nil && live.WorkingDir != "" {
			return live.WorkingDir
		}
		snap, err := h.gs.GetLatestGitStateBySession(ctx, sessionID)
		if err == nil && snap != nil {
			return snap.WorkingDirectory
		}
	}
	snap, err := h.gs.GetLatestGitState(ctx, name)
	if err == nil && snap != nil {
		return snap.WorkingDirectory
	}
	return ""
}

// resolveGitRoot returns the git toplevel for the agent's working directory.
func (h *SessionsHandler) resolveGitRoot(ctx context.Context, name, agentType, sessionID string) string {
	workdir := h.resolveWorkdir(ctx, name, agentType, sessionID)
	if workdir == "" {
		return ""
	}
	return trustedGitRoot(ctx, workdir)
}

// filesCacheFloor is how long a changed-files list stays fresh when nothing
// refreshes it in the background: a diff mode the git poller does not
// compute, or polling turned off.
const filesCacheFloor = 30 * time.Second

// filesCacheMaxAge is how old a cached changed-files list may be before the
// Files handler recomputes it. The git poller rewrites only the branch_point
// list, every git_poll_interval_s, so that list stays fresh for two poll
// intervals; any other list was written by a refresh and is kept briefly.
func (h *SessionsHandler) filesCacheMaxAge(ctx context.Context, diffMode string) time.Duration {
	if diffMode != "branch_point" {
		return filesCacheFloor
	}
	settings, _ := h.ss.GetSettings(ctx)
	interval := background.GitPollInterval(settings, h.cfg.GitPollerIntervalS)
	if interval <= 0 {
		return filesCacheFloor
	}
	if interval < background.MinGitPollInterval {
		interval = background.MinGitPollInterval
	}
	return max(2*interval, filesCacheFloor)
}

// filesCacheFresh reports whether a cached list was written within maxAge.
// Every row of one list shares its recorded_at.
func filesCacheFresh(files []store.ChangedFile, maxAge time.Duration, now time.Time) bool {
	if len(files) == 0 {
		return false
	}
	recorded, err := time.Parse(store.ISOFormat, files[0].RecordedAt)
	if err != nil {
		return false
	}
	return now.Sub(recorded) <= maxAge
}

// Files returns changed files for a live agent.
// GET /api/sessions/live/{name}/files
// Returns the cached list for the user's current diff mode while it is
// fresh (see filesCacheMaxAge); otherwise computes it again, as a refresh
// does, so a list from before a rebase, merge or branch switch is never
// served as current.
func (h *SessionsHandler) Files(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)

	diffMode := h.getDiffMode(r.Context())
	if diffMode == "" {
		diffMode = "branch_point"
	}

	files, found, err := h.gs.GetChangedFiles(r.Context(), name, sidPtr, diffMode)
	if err == nil && found && filesCacheFresh(files, h.filesCacheMaxAge(r.Context(), diffMode), time.Now()) {
		writeJSON(w, http.StatusOK, map[string]any{"agent_name": name, "files": hubVisibleFiles(r, files), "diff_mode": diffMode})
		return
	}

	sid := ""
	if sidPtr != nil {
		sid = *sidPtr
	}
	h.refreshFilesInner(w, r, name, sid)
}

// RefreshFiles runs fresh git queries and merges agent Write/Edit events.
// POST /api/sessions/live/{name}/files/refresh
func (h *SessionsHandler) RefreshFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	h.refreshFilesInner(w, r, name, body.SessionID)
}

// trackedPaths reports which of the given repo-relative paths git tracks. It
// asks only about those paths, so the cost follows the number of candidates
// rather than the size of the repository. An empty list costs nothing.
func trackedPaths(ctx context.Context, workdir string, paths []string) map[string]bool {
	tracked := make(map[string]bool, len(paths))
	if len(paths) == 0 {
		return tracked
	}

	args := append([]string{"ls-files", "-z", "--"}, paths...)
	out, err := gitCmd(ctx, workdir, args...).Output()
	if err != nil {
		// Unknown tracked status: treat every candidate as untracked, which at
		// worst shows a clean file in the list rather than hiding a dirty one.
		slog.Warn("git ls-files (tracked) failed", "workdir", workdir, "error", err)
		return tracked
	}
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 {
			tracked[string(f)] = true
		}
	}
	return tracked
}

// gitCmd builds a git command for the changed-files path. --no-optional-locks
// keeps a read-only query from refreshing and rewriting the index, which on a
// large repo means disk writes and lock contention on every poll.
func gitCmd(ctx context.Context, workdir string, args ...string) *exec.Cmd {
	return gitutil.Command(ctx, workdir, args...)
}

// refreshFilesInner contains the shared logic for computing fresh changed files.
func (h *SessionsHandler) refreshFilesInner(w http.ResponseWriter, r *http.Request, name, sessionID string) {
	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Could not determine working directory", "files": []any{}})
		return
	}

	// New files' line counts are read through the working tree held as a root (#3596 W5): a
	// symlinked folder on the way, a symlink, a FIFO or a device is never read. If the tree can't
	// be opened, new files count 0 lines.
	root, rootErr := safefs.OpenRoot(workdir)
	if rootErr == nil {
		defer root.Close()
	}
	lineCount := func(rel string) int {
		if rootErr != nil {
			return 0
		}
		return gitutil.NewFileLineCountIn(root, rel)
	}

	// Run git diff --numstat to get changed files
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	diffMode := h.getDiffMode(r.Context())
	base := gitutil.GetDiffBase(ctx, workdir, diffMode)
	out, err := gitCmd(ctx, workdir, "diff", base, "--numstat").Output()
	fileMap := make(map[string]store.ChangedFile)
	if err != nil {
		slog.Warn("git diff --numstat failed", "agent_name", name, "workdir", workdir, "error", err)
	} else {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) < 3 {
				continue
			}
			adds, _ := strconv.Atoi(parts[0])
			dels, _ := strconv.Atoi(parts[1])
			fileMap[parts[2]] = store.ChangedFile{Filepath: parts[2], Additions: adds, Deletions: dels, Status: "M"}
		}
	}

	// Include untracked files
	untrackedSet := make(map[string]bool)
	untrackedOut, err := gitCmd(ctx, workdir, "ls-files", "--others", "--exclude-standard").Output()
	if err != nil {
		slog.Warn("git ls-files (untracked) failed", "agent_name", name, "workdir", workdir, "error", err)
	} else {
		counted := 0
		for _, f := range strings.Split(strings.TrimSpace(string(untrackedOut)), "\n") {
			if f == "" {
				continue
			}
			untrackedSet[f] = true
			if _, exists := fileMap[f]; !exists {
				adds := 0
				if counted < gitutil.MaxNewFileStatFiles {
					adds = lineCount(f)
					counted++
				}
				fileMap[f] = store.ChangedFile{Filepath: f, Additions: adds, Deletions: 0, Status: "??"}
			}
		}
	}

	// Files the agent wrote or edited that git has not already reported.
	// Collect the candidates first so their tracked status can be asked for by
	// pathspec: this used to list every tracked file in the repo to answer a
	// question about a handful of paths, which on a monorepo is the single
	// most expensive thing here.
	sidPtr := strPtr(sessionID)
	events, _ := h.ts.ListAgentEvents(r.Context(), name, 200, sidPtr)
	candidates := make([]string, 0, 16)
	seen := make(map[string]bool)
	for _, ev := range events {
		if ev.ToolName == nil || (*ev.ToolName != "Write" && *ev.ToolName != "Edit") {
			continue
		}
		if ev.DetailJSON == nil {
			continue
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(*ev.DetailJSON), &detail); err != nil {
			continue
		}
		fp, ok := detail["file_path"].(string)
		if !ok || fp == "" {
			continue
		}
		rel, err := filepath.Rel(workdir, fp)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if _, exists := fileMap[rel]; exists || seen[rel] {
			continue
		}
		seen[rel] = true
		candidates = append(candidates, rel)
	}

	trackedSet := trackedPaths(ctx, workdir, candidates)

	for _, rel := range candidates {
		// Skip files that no longer exist on disk, or aren't a regular file inside the tree (an
		// event's file_path comes from a hook payload; looked up through the root, #3596 W5)
		if rootErr != nil {
			continue
		}
		info, statErr := root.Lstat(rel)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		// Skip tracked files not in fileMap — they have no diff (clean)
		if trackedSet[rel] && !untrackedSet[rel] {
			continue
		}
		fileMap[rel] = store.ChangedFile{
			Filepath:  rel,
			Additions: lineCount(rel),
			Deletions: 0,
			Status:    "??",
		}
	}

	// Annotate files with agent attribution from agent_events
	if sessionID != "" {
		if edits, err := h.ts.GetFileEdits(r.Context(), sessionID, workdir); err == nil {
			for fp, agents := range edits {
				if f, exists := fileMap[fp]; exists {
					f.Agents = agents
					f.Source = "git"
					fileMap[fp] = f
				}
			}
		}
	}

	// Set source for files without explicit attribution
	for fp, f := range fileMap {
		if f.Source == "" {
			f.Source = "git"
			fileMap[fp] = f
		}
	}

	// Update DB cache
	files := make([]store.ChangedFile, 0, len(fileMap))
	for _, f := range fileMap {
		files = append(files, f)
	}
	if diffMode == "" {
		diffMode = "branch_point"
	}
	h.gs.ReplaceChangedFiles(r.Context(), name, workdir, files, sidPtr, diffMode)

	writeJSON(w, http.StatusOK, map[string]any{"agent_name": name, "files": hubVisibleFiles(r, files), "diff_mode": diffMode})
}

// Diff returns the unified diff for a single file.
// GET /api/sessions/live/{name}/diff
func (h *SessionsHandler) Diff(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	fp := r.URL.Query().Get("filepath")
	sessionID := r.URL.Query().Get("session_id")

	if fp == "" || strings.HasPrefix(fp, "-") || strings.ContainsAny(fp, "\x00") {
		errBadRequest(w, "invalid filepath")
		return
	}

	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		errBadRequest(w, "Could not determine working directory")
		return
	}

	strict := strictBounds(r)

	// Path traversal protection
	fullPath, _ := filepath.Abs(filepath.Join(workdir, fp))
	if !isPathWithinDir(workdir, fullPath) {
		if strict {
			errNotFound(w, "File not found")
		} else {
			errForbidden(w, "path traversal not allowed")
		}
		return
	}

	// Anything but this computer's own dashboard (Coral Hub, an API key, a remote client)
	// gets the same bounds as file-content (#3558, Security's #3556 finding 1): a root that
	// isn't the filesystem or the home folder, no sensitive path, no dot-file or dot-folder
	// (.env.local, .git/config), a size cap, a filtered patch and no local path. Refused
	// files look exactly like missing ones.
	if strict && diffPathRefused(workdir, fp, fullPath) {
		errNotFound(w, "File not found")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	base := gitutil.GetDiffBase(ctx, workdir, h.getDiffMode(r.Context()))
	gitArgs := []string{"diff", base, "--", fp}
	if strict {
		// The filepath is a name, never a pathspec: '*' or ':(glob)**' would diff the whole
		// tree (QA's #3558 review).
		gitArgs = append([]string{"--literal-pathspecs"}, gitArgs...)
	}
	out, err := gitutil.Command(ctx, workdir, gitArgs...).Output()
	diffText := ""
	if err != nil {
		slog.Warn("git diff failed", "agent_name", name, "filepath", fp, "error", err)
	} else {
		diffText = string(out)
	}

	// For untracked files, show the file content as a "new file" diff
	if diffText == "" {
		fullPath := filepath.Join(workdir, fp)
		realFull, err := safefs.Resolve(fullPath)
		realWork, _ := safefs.Resolve(workdir)
		if err == nil && strings.HasPrefix(realFull, realWork+string(os.PathSeparator)) {
			if info, err := os.Stat(realFull); err == nil && !info.IsDir() {
				if strict && (info.Size() > maxFileBytes() || isBinaryFile(realFull)) {
					diffText = string(diffNote("new file %q, %d bytes, not shown", fp, info.Size()))
				} else if data, err := os.ReadFile(realFull); err == nil {
					lines := strings.Split(string(data), "\n")
					diffText = fmt.Sprintf("diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n",
						fp, fp, fp, len(lines))
					for _, line := range lines {
						diffText += "+" + line + "\n"
					}
				}
			}
		}
	}

	if strict {
		diffText = string(capDiff(filterPatch([]byte(diffText)), int(maxFileBytes())))
		writeJSON(w, http.StatusOK, map[string]any{"filepath": fp, "diff": diffText})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"filepath": fp, "diff": diffText, "working_directory": workdir})
}

// ChangesDiff returns a downloadable unified diff for all changes in an
// agent's current working directory, including committed changes since the
// configured diff base, working-tree changes, and untracked files.
// GET /api/sessions/live/{name}/changes.diff
func (h *SessionsHandler) ChangesDiff(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sessionID := r.URL.Query().Get("session_id")
	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		errBadRequest(w, "Could not determine working directory")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Anything but this computer's own dashboard (Coral Hub, a Bearer-key proxy, curl): filtered
	// and capped, no local path (#3555), and only for a working tree that isn't the home folder
	// or the filesystem root (#3596 W3).
	tunneled := strictBounds(r)
	if tunneled && !rootAllowed(realPath(workdir)) {
		errNotFound(w, "Could not generate changes.diff")
		return
	}
	gen := h.generateChangesDiff
	if tunneled {
		gen = h.generatePersistedDiff
	}
	diff, err := gen(ctx, workdir)
	if err != nil {
		slog.Warn("git changes diff failed", "agent_name", name, "workdir", workdir, "error", err)
		errInternalServer(w, "Could not generate changes.diff")
		return
	}

	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="changes.diff"`)
	if !tunneled {
		w.Header().Set("X-Coral-Working-Directory", workdir)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(diff)
}

func (h *SessionsHandler) generateChangesDiff(ctx context.Context, workdir string) ([]byte, error) {
	base := gitutil.GetDiffBase(ctx, workdir, h.getDiffMode(ctx))
	diff, err := gitutil.Command(ctx, workdir, "diff", "--binary", base, "--").Output()
	if err != nil {
		return nil, err
	}

	// A normal git diff omits untracked files. Render each as a new-file patch.
	untracked, err := gitutil.Command(ctx, workdir, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, err
	}
	for _, rawPath := range bytes.Split(untracked, []byte{0}) {
		if len(rawPath) == 0 {
			continue
		}
		cmd := gitutil.Command(ctx, workdir, "diff", "--no-index", "--binary", "--", os.DevNull, string(rawPath))
		fileDiff, fileErr := cmd.Output()
		// git diff --no-index returns 1 when differences were found.
		if fileErr != nil {
			if exitErr, ok := fileErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
				return nil, fmt.Errorf("diff untracked file %q: %w", string(rawPath), fileErr)
			}
		}
		if len(diff) > 0 && diff[len(diff)-1] != '\n' {
			diff = append(diff, '\n')
		}
		diff = append(diff, fileDiff...)
	}
	return diff, nil
}

// SessionChanges returns a session's unified diff using only its stable ID.
// Live sessions are generated from their current working directory; stopped
// sessions fall back to the artifact captured when they were terminated.
// GET /api/sessions/{sessionID}/changes
func (h *SessionsHandler) SessionChanges(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if !safefs.IsSafeID(sessionID) { // one path element: never ".", ".." or a separator (#3596 W4)
		errBadRequest(w, "invalid session_id")
		return
	}

	strict := strictBounds(r)
	workdir := h.resolveGitRoot(r.Context(), "", "", sessionID)
	if workdir != "" && (!strict || rootAllowed(realPath(workdir))) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		gen := h.generateChangesDiff
		if strict {
			gen = h.generatePersistedDiff // not this computer's dashboard: filtered and capped (#3555, #3596 W3)
		}
		if diff, err := gen(ctx, workdir); err == nil {
			writeSessionChanges(w, r, diff, workdir)
			return
		} else {
			slog.Warn("git session changes failed", "session_id", sessionID, "workdir", workdir, "error", err)
		}
	}

	data, err := os.ReadFile(h.sessionChangesArtifactPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			errNotFound(w, "session changes not found")
			return
		}
		errInternalServer(w, err.Error())
		return
	}
	if strict {
		data = safeDiff(data) // a stored diff (also one from before #3555), filtered (#3596 W3)
	}
	writeSessionChanges(w, r, data, "")
}

func writeSessionChanges(w http.ResponseWriter, r *http.Request, diff []byte, workdir string) {
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="changes.diff"`)
	if workdir != "" && !strictBounds(r) { // a local path only for this computer's dashboard (#3555, #3596 W3)
		w.Header().Set("X-Coral-Working-Directory", workdir)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(diff)
}

// PersistTaskChanges stores a board task's final patch before its working
// directory can be cleaned up.
func (h *SessionsHandler) PersistTaskChanges(ctx context.Context, task *board.Task, artifactPath string) error {
	if task == nil || task.SessionID == nil || *task.SessionID == "" {
		return fmt.Errorf("task has no session_id")
	}
	workdir := h.resolveGitRoot(ctx, "", "", *task.SessionID)
	if workdir == "" {
		return fmt.Errorf("could not determine working directory")
	}
	diff, err := h.generatePersistedDiff(ctx, workdir) // filtered and capped (#3555)
	if err != nil {
		return err
	}
	return writeChangesArtifact(artifactPath, diff)
}

func writeChangesArtifact(artifactPath string, diff []byte) error {
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		return err
	}
	// Identical diffs are stored once (#3555): the content goes to
	// artifacts/diff-objects/<sha256> and the task's or session's changes.diff is a hard link
	// to it, so the same unchanged checkout doesn't take its full size again for every task,
	// and removing one task's diff never touches another's.
	if obj, err := storeDiffObject(artifactPath, diff); err == nil {
		tmp := artifactPath + ".link-tmp"
		os.Remove(tmp)
		if os.Link(obj, tmp) == nil {
			if err := os.Rename(tmp, artifactPath); err == nil {
				return nil
			}
			os.Remove(tmp)
		}
	}
	// No hard links here: a plain copy.
	tmp, err := os.CreateTemp(filepath.Dir(artifactPath), ".changes-*.diff")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(diff); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, artifactPath)
}

// storeDiffObject stores diff once under <coral>/artifacts/diff-objects/<sha256> (found from
// artifactPath: .../artifacts/{tasks,sessions}/<id>/changes.diff) and returns its path.
func storeDiffObject(artifactPath string, diff []byte) (string, error) {
	root := filepath.Dir(filepath.Dir(filepath.Dir(artifactPath))) // .../artifacts
	if filepath.Base(root) != "artifacts" {
		return "", fmt.Errorf("not under artifacts/")
	}
	sum := sha256.Sum256(diff)
	dir := filepath.Join(root, "diff-objects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	obj := filepath.Join(dir, hex.EncodeToString(sum[:]))
	if _, err := os.Stat(obj); err == nil {
		return obj, nil
	}
	if err := writeFileAtomic(obj, diff, 0o600); err != nil {
		return "", err
	}
	return obj, nil
}

func (h *SessionsHandler) sessionChangesArtifactPath(sessionID string) string {
	return filepath.Join(h.cfg.CoralDir(), "artifacts", "sessions", sessionID, "changes.diff")
}

func (h *SessionsHandler) persistSessionChanges(ctx context.Context, sessionID, workdir string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("session_id required")
	}
	if workdir == "" {
		workdir = h.resolveGitRoot(ctx, "", "", sessionID)
	} else {
		workdir = gitutil.ResolveGitRoot(ctx, workdir)
	}
	if workdir == "" {
		return "", fmt.Errorf("could not determine working directory")
	}
	if !gitutil.IsWorkTree(ctx, workdir) {
		return "", errNoGitCheckout
	}
	diff, err := h.generatePersistedDiff(ctx, workdir)
	if err != nil {
		return "", err
	}
	path := h.sessionChangesArtifactPath(sessionID)
	if err := writeChangesArtifact(path, diff); err != nil {
		return "", err
	}
	return path, nil
}

// errNoGitCheckout: the session's folder isn't a git checkout, so there are no changes
// to snapshot. Not a failure: the kill answers with no artifact and no error (#3520).
var errNoGitCheckout = errors.New("not a git checkout")

// changesArtifactProblem is what the kill answer (and the dashboard's toast) says when
// the snapshot failed: a plain sentence, never git's raw error, which goes to the log
// (#3520, QA's bug bash F6: "exit status 129").
const changesArtifactProblem = "git couldn't produce the diff (details in coral.log)"

// SessionChangesArtifact serves a patch captured before a session was killed.
// GET /api/sessions/{sessionID}/changes.diff
func (h *SessionsHandler) SessionChangesArtifact(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if !safefs.IsSafeID(sessionID) { // one path element: never ".", ".." or a separator (#3596 W4)
		errBadRequest(w, "invalid session_id")
		return
	}
	data, err := os.ReadFile(h.sessionChangesArtifactPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			errNotFound(w, "session changes artifact not found")
			return
		}
		errInternalServer(w, err.Error())
		return
	}
	if strictBounds(r) {
		data = safeDiff(data) // not this computer's dashboard: filtered and capped, old diffs too (#3555, #3596 W3)
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="changes.diff"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// SearchFiles searches for files in the agent's working directory.
// GET /api/sessions/live/{name}/search-files
//
// Query parameters:
//
//	q          - search query (fuzzy mode)
//	dir        - directory path to list (directory browsing mode)
//	session_id - optional session identifier
//
// When 'dir' is provided, returns entries in that directory with type info
// (directory browsing mode). When 'q' is provided, returns fuzzy matches
// (search mode). When neither is provided, returns the first 50 files.
func (h *SessionsHandler) SearchFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	query := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("q")))
	dir := r.URL.Query().Get("dir")
	sessionID := r.URL.Query().Get("session_id")
	if r.URL.Query().Get("view") == "explorer" {
		serveExplorerDirectory(w, r, h.resolveGitRoot(r.Context(), name, "", sessionID), dir, strictBounds(r))
		return
	}

	// Read file search limit from user settings (default 500)
	fileSearchLimit := 500
	if settings, err := h.ss.GetSettings(r.Context()); err == nil {
		if v, ok := settings["file_search_limit"]; ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				fileSearchLimit = n
			}
		}
	}

	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		writeJSON(w, http.StatusOK, map[string]any{"files": []string{}})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Directory browsing mode: list entries in a specific directory
	if dir != "" {
		h.searchFilesDir(w, ctx, workdir, dir, query, fileSearchLimit, strictBounds(r))
		return
	}

	out, err := gitutil.Command(ctx, workdir, "ls-files", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"files": []string{}})
		return
	}

	allFiles := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(allFiles) == 1 && allFiles[0] == "" {
		allFiles = nil
	}
	if strictBounds(r) {
		if !rootAllowed(realPath(workdir)) {
			allFiles = nil
		}
		kept := allFiles[:0]
		for _, fp := range allFiles {
			if !sensitivePath(fp) {
				kept = append(kept, fp)
			}
		}
		allFiles = kept
	}

	if query == "" {
		if len(allFiles) > fileSearchLimit {
			allFiles = allFiles[:fileSearchLimit]
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": allFiles})
		return
	}

	// Score matches: basename match > basename contains > path contains
	type scored struct {
		score int
		path  string
	}
	var matches []scored
	for _, fp := range allFiles {
		fpLower := strings.ToLower(fp)
		basename := strings.ToLower(filepath.Base(fp))
		if strings.Contains(fpLower, query) {
			s := 2
			if basename == query {
				s = 0
			} else if strings.Contains(basename, query) {
				s = 1
			}
			matches = append(matches, scored{s, fp})
		}
	}
	// Sort by score then path
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score < matches[j].score
		}
		return matches[i].path < matches[j].path
	})
	result := make([]string, 0, fileSearchLimit)
	for i, m := range matches {
		if i >= fileSearchLimit {
			break
		}
		result = append(result, m.path)
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": result})
}

// searchFilesDir lists filesystem entries in a specific directory for
// directory browsing mode. Uses os.ReadDir for direct filesystem access,
// like ls/tab-completion. Returns entries with name, path, and type
// (dir/file). Directories are listed first, then files. An optional
// filter (from the 'q' parameter) restricts results to matching names.
func (h *SessionsHandler) searchFilesDir(w http.ResponseWriter, _ context.Context, workdir, dir, filter string, limit int, strict bool) {
	// Normalize dir: strip leading/trailing slashes, use "." for root
	dir = strings.TrimPrefix(dir, "/")
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}

	// Prevent path traversal
	if strings.Contains(dir, "..") {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}})
		return
	}

	// Build the absolute path to read
	absDir := workdir
	if dir != "." {
		absDir = filepath.Join(workdir, dir)
	}

	// A symlink inside the workspace must not lead the listing out of it.
	realWD, realDir := realPath(workdir), realPath(absDir)
	if !within(realWD, realDir) || (strict && (!rootAllowed(realWD) || sensitivePath(realDir))) {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "dir": dir})
		return
	}
	dirEntries, err := os.ReadDir(realDir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "dir": dir})
		return
	}

	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"` // "dir" or "file"
	}

	var dirs []entry
	var files []entry
	showHidden := strings.HasPrefix(filter, ".")

	for _, de := range dirEntries {
		name := de.Name()
		if strict && !listingVisible(realDir, name) {
			continue
		}

		// Skip hidden files/dirs unless filter starts with .
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}

		// Skip non-text files and files over 1MB
		if !de.IsDir() {
			if !isTextFile(name) {
				continue
			}
			if info, err := de.Info(); err == nil && info.Size() > 1<<20 {
				continue
			}
		}

		// Apply filter if provided
		if filter != "" && !strings.Contains(strings.ToLower(name), filter) {
			continue
		}

		entryPath := name
		if dir != "." {
			entryPath = dir + "/" + name
		}

		if de.IsDir() {
			dirs = append(dirs, entry{
				Name: name + "/",
				Path: entryPath,
				Type: "dir",
			})
		} else {
			files = append(files, entry{
				Name: name,
				Path: entryPath,
				Type: "file",
			})
		}
	}

	// Sort dirs and files alphabetically by name (case-insensitive)
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name) })

	// Combine: directories first, then files
	entries := make([]entry, 0, len(dirs)+len(files))
	entries = append(entries, dirs...)
	entries = append(entries, files...)

	if len(entries) > limit {
		entries = entries[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"dir":     dir,
	})
}

// textExtensions is a whitelist of file extensions known to be text/source
// files safe to preview in the web UI. Files not matching this list are
// excluded from directory browsing results to prevent loading binary files.
var textExtensions = map[string]bool{
	// Go
	".go": true, ".mod": true, ".sum": true,
	// JavaScript / TypeScript
	".js": true, ".ts": true, ".tsx": true, ".jsx": true, ".mjs": true, ".cjs": true,
	// Web
	".html": true, ".htm": true, ".css": true, ".scss": true, ".less": true,
	// Data / Config
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".xml": true,
	".csv": true, ".ini": true, ".cfg": true, ".conf": true, ".properties": true,
	// Documentation
	".md": true, ".txt": true, ".rst": true, ".adoc": true,
	// Python
	".py": true, ".pyi": true, ".pyx": true,
	// Ruby
	".rb": true, ".erb": true, ".rake": true, ".gemspec": true,
	// Rust
	".rs": true,
	// C / C++
	".c": true, ".h": true, ".cpp": true, ".hpp": true, ".cc": true, ".hh": true,
	// Java / JVM
	".java": true, ".kt": true, ".kts": true, ".scala": true, ".gradle": true,
	// Shell
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	// SQL / Query
	".sql": true, ".graphql": true, ".gql": true,
	// Protocol / Schema
	".proto": true, ".avro": true, ".thrift": true,
	// Environment / Config files
	".env": true, ".envrc": true,
	".gitignore": true, ".gitattributes": true, ".gitmodules": true,
	".dockerignore": true, ".editorconfig": true,
	".eslintrc": true, ".prettierrc": true, ".babelrc": true,
	// Swift / Objective-C
	".swift": true, ".m": true, ".mm": true,
	// Other
	".log": true, ".lock": true, ".patch": true, ".diff": true,
	".tf": true, ".hcl": true, // Terraform
	".lua": true, ".vim": true, ".el": true,
	".r": true, ".R": true, ".jl": true, // R, Julia
	".ex": true, ".exs": true, // Elixir
	".hs": true, ".cabal": true, // Haskell
	".pl": true, ".pm": true, // Perl
	".php": true, ".twig": true,
	".dart": true, ".svelte": true, ".vue": true,
	".nix": true, ".dhall": true,
	".tmpl": true, ".tpl": true, // Go templates
	".plist": true, // macOS property lists
}

// textFileNames are known text files without extensions.
var textFileNames = map[string]bool{
	"Makefile": true, "Dockerfile": true, "Containerfile": true,
	"LICENSE": true, "README": true, "CHANGELOG": true,
	"Gemfile": true, "Rakefile": true, "Procfile": true,
	"Vagrantfile": true, "Brewfile": true,
	"CLAUDE.md": true, "MEMORY.md": true,
	".gitignore": true, ".gitattributes": true, ".gitmodules": true,
	".dockerignore": true, ".editorconfig": true,
	".eslintrc": true, ".prettierrc": true,
}

// isTextFile returns true if the filename has a known text file extension
// or is a known extensionless text file.
func isTextFile(name string) bool {
	if textFileNames[name] {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false
	}
	return textExtensions[ext]
}

// Git returns recent git snapshots for a live agent.
// GET /api/sessions/live/{name}/git
func (h *SessionsHandler) Git(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sessionID := r.URL.Query().Get("session_id")
	limit := clampLimit(queryInt(r, "limit", 20), 20, 100)

	var snapshots []store.GitSnapshot
	var err error
	if sessionID != "" {
		snapshots, err = h.gs.GetGitSnapshotsForSession(r.Context(), sessionID, limit)
	} else {
		snapshots, err = h.gs.GetGitSnapshots(r.Context(), name, limit)
	}
	if err != nil || snapshots == nil {
		snapshots = []store.GitSnapshot{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_name": name, "snapshots": snapshots})
}

// ── Commands ────────────────────────────────────────────────────────────

// Send sends a command to a live tmux session.
// POST /api/sessions/live/{name}/send
func (h *SessionsHandler) Send(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		Command   string `json:"command"`
		AgentType string `json:"agent_type"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Command == "" {
		errBadRequest(w, "No command provided")
		return
	}
	// Input always names its exact target (session_id + agent_type). A bare name
	// would reach whatever tmux pane has it, sleeping or archived agents included.
	if _, status, message := h.validateExactLiveTarget(r.Context(), name, body.AgentType, body.SessionID, false); status != 0 {
		if status == http.StatusServiceUnavailable {
			writeJSON(w, status, map[string]any{"error": message + " No input was sent.", "code": "agent_metadata_unavailable", "not_sent": true})
		} else {
			writeExactTargetError(w, status, message)
		}
		return
	}

	send := func(ctx context.Context) error {
		return h.terminal.SendInput(ctx, name, body.Command, body.AgentType, body.SessionID)
	}
	// A person (the dashboard, Coral Hub at Interact, the operator's signed CLI)
	// types directly. Anything else (an agent's tool, a script, an API key) is
	// automation: it goes through the automated-input gate and is held while the
	// agent waits on a prompt, so it can't answer one (#3431).
	if !humanInputCaller(r) {
		if held := h.sendAutomatedHTTP(w, r, name, body.AgentType, body.SessionID, body.Command, "/send", send); held {
			return
		}
	} else if err := send(r.Context()); err != nil {
		writeTerminalError(w, err)
		return
	}
	// The terminal accepted the input over HTTP: the narrowly labelled
	// first_prompt_submitted milestone. It says nothing about what the agent did.
	tracking.TrackOnce(tracking.EventFirstPromptSubmitted, map[string]string{"source": "http_send"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "command": body.Command})
}

// Keys sends raw tmux key names to a session.
// POST /api/sessions/live/{name}/keys
func (h *SessionsHandler) Keys(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		Keys      []string `json:"keys"`
		AgentType string   `json:"agent_type"`
		SessionID string   `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Keys) == 0 {
		errBadRequest(w, "keys must be a non-empty list")
		return
	}
	// As for Send: keys always name their exact target.
	if _, status, message := h.validateExactLiveTarget(r.Context(), name, body.AgentType, body.SessionID, false); status != 0 {
		writeExactTargetError(w, status, message)
		return
	}

	send := func(ctx context.Context) error {
		return h.terminal.SendRawInput(ctx, name, body.Keys, body.AgentType, body.SessionID)
	}
	// As for Send: keys from anything but a person go through the gate (#3431).
	if !humanInputCaller(r) {
		if held := h.sendAutomatedHTTP(w, r, name, body.AgentType, body.SessionID, strings.Join(body.Keys, " "), "/keys", send); held {
			return
		}
	} else if err := send(r.Context()); err != nil {
		writeTerminalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "keys": body.Keys})
}

// Resize resizes the tmux pane width.
// POST /api/sessions/live/{name}/resize
func (h *SessionsHandler) Resize(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		Columns   int    `json:"columns"`
		AgentType string `json:"agent_type"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Columns < 10 {
		errBadRequest(w, "columns must be >= 10")
		return
	}
	// As for Send: a resize always names its exact target (#3211).
	if _, status, message := h.validateExactLiveTarget(r.Context(), name, body.AgentType, body.SessionID, false); status != 0 {
		writeExactTargetError(w, status, message)
		return
	}

	if err := h.terminal.ResizeSession(r.Context(), name, body.Columns, body.AgentType, body.SessionID); err != nil {
		writeTerminalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "columns": body.Columns})
}

// ── Lifecycle ───────────────────────────────────────────────────────────

// Kill terminates a tmux session.
// POST /api/sessions/live/{name}/kill
func (h *SessionsHandler) Kill(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	name := chi.URLParam(r, "name")
	var body struct {
		AgentType string `json:"agent_type"`
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if namesNoSession(name, body.SessionID) {
		writeSessionIDRequired(w)
		return
	}

	slog.Info("killing agent", "agent_name", name, "session_id", body.SessionID, "agent_type", body.AgentType)

	// Use a background context for DB operations so they complete even if
	// the HTTP request context is cancelled (e.g., during tmux command failures).
	bgCtx := context.Background()

	// Look up the live session before deleting so we can clean up board/worktree state
	var boardName, sessionWorkdir string
	var wasSleeping, sessionFound bool
	var worktreePathForCleanup, worktreeRepoForCleanup string
	if body.SessionID != "" {
		if ls, err := h.ss.GetLiveSession(bgCtx, body.SessionID); err == nil && ls != nil {
			sessionFound = true
			sessionWorkdir = ls.WorkingDir
			if ls.BoardName != nil {
				boardName = *ls.BoardName
			}
			wasSleeping = ls.IsSleeping == 1
			if ls.WorktreePath != nil {
				worktreePathForCleanup = *ls.WorktreePath
			}
			if ls.WorktreeRepo != nil {
				worktreeRepoForCleanup = *ls.WorktreeRepo
			}
		}
	}

	// Snapshot the checkout before unregistering the session or removing an
	// owned worktree. A snapshot failure is reported but does not make agents
	// in non-git directories impossible to terminate.
	changesArtifact := ""
	artifactError := ""
	if body.SessionID != "" && body.AgentType != at.Terminal {
		artifactCtx, cancel := context.WithTimeout(bgCtx, 30*time.Second)
		if path, err := h.persistSessionChanges(artifactCtx, body.SessionID, sessionWorkdir); errors.Is(err, errNoGitCheckout) {
			slog.Info("no changes artifact: the session's folder isn't a git checkout", "session_id", body.SessionID)
		} else if err != nil {
			artifactError = changesArtifactProblem
			slog.Warn("persist session changes artifact failed", "session_id", body.SessionID, "error", err)
		} else {
			changesArtifact = path
		}
		cancel()
	}

	// Mark the team member as stopped (if this session belongs to a team)
	if body.SessionID != "" && h.teamStore != nil {
		if member, err := h.teamStore.GetMemberBySessionID(bgCtx, body.SessionID); err == nil && member != nil {
			h.teamStore.UpdateMemberStatus(bgCtx, member.ID, "stopped", member.SessionID)
		}
	}

	// Unregister from DB BEFORE killing the tmux/pty session.
	// This ordering is critical: KillSession may hang or take a long time
	// (e.g., if the tmux socket is in a bad state), and we must ensure the
	// DB row is removed so sleeping sessions don't reappear on server restart.
	if body.SessionID != "" {
		h.chatMeta.forget(body.SessionID)
		if err := h.ss.UnregisterLiveSession(bgCtx, body.SessionID); err != nil {
			log.Printf("[kill] failed to unregister session %s: %v", body.SessionID, err)
		}
	}

	// Clear board pause state if no sleeping sessions remain.
	// The board is paused during sleep, so we must unpause when either:
	// (a) all sessions are gone, or
	// (b) only awake sessions remain (sleeping agents were killed).
	if boardName != "" && h.boardHandler != nil {
		remaining, _ := h.ss.CountBoardSessions(bgCtx, boardName)
		if remaining == 0 {
			h.boardHandler.SetPaused(boardName, false)
			if wasSleeping {
				log.Printf("[kill] cleared board pause state for sleeping board %q (no remaining sessions)", boardName)
			}
		} else if wasSleeping {
			// There are remaining sessions but we just killed a sleeping one.
			// Check if any sleeping sessions are left; if not, unpause.
			var sleepingCount int
			allSessions, _ := h.ss.GetAllLiveSessions(bgCtx)
			for _, ls := range allSessions {
				if ls.BoardName != nil && *ls.BoardName == boardName && ls.IsSleeping == 1 {
					sleepingCount++
				}
			}
			if sleepingCount == 0 {
				h.boardHandler.SetPaused(boardName, false)
				log.Printf("[kill] cleared board pause state for board %q (no sleeping sessions remain, %d awake)", boardName, remaining)
			}
		}
	}

	// Clean up board state file so it doesn't accumulate over time
	removeBoardStateFile(name, h.cfg)

	// Clean up temp files (system prompts, settings, action prompts): only a session that was
	// live, by its own id (#3598).
	if sessionFound {
		agent.CleanupTempFiles(body.SessionID)
	}

	// Sleeping agents have no tmux session (it was terminated when they were
	// put to sleep). Skip all tmux operations — calling KillSession here
	// risks the fuzzy FindPane fallback matching a different agent's session.
	if wasSleeping {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changes_artifact": changesArtifact, "changes_artifact_error": artifactError})
		return
	}

	// Kill tmux/pty session for awake agents
	h.terminal.KillSession(r.Context(), name, body.AgentType, body.SessionID)

	// Clean up git worktree if this was the last session using it
	if worktreePathForCleanup != "" && worktreeRepoForCleanup != "" {
		// Check if any remaining sessions use this worktree
		remaining, _ := h.ss.CountSessionsWithWorktree(bgCtx, worktreePathForCleanup)
		if remaining == 0 {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := gitutil.Command(ctx, worktreeRepoForCleanup, "worktree", "remove", "--force", worktreePathForCleanup)
				if out, err := cmd.CombinedOutput(); err != nil {
					log.Printf("[kill] failed to remove worktree %s: %s", worktreePathForCleanup, string(out))
				} else {
					log.Printf("[kill] cleaned up worktree: %s", worktreePathForCleanup)
				}
			}()
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changes_artifact": changesArtifact, "changes_artifact_error": artifactError})
}

func sendLaunchCommand(ctx context.Context, terminal ptymanager.SessionTerminal, target, command string) error {
	if err := terminal.SendToTarget(ctx, target, command); err != nil {
		return fmt.Errorf("launch command delivery to %s failed: %w", target, err)
	}
	return nil
}

// Restart restarts the agent session.
// POST /api/sessions/live/{name}/restart
func (h *SessionsHandler) Restart(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	name := chi.URLParam(r, "name")
	var body struct {
		AgentType    string              `json:"agent_type"`
		ExtraFlags   string              `json:"extra_flags"`
		SessionID    string              `json:"session_id"`
		Prompt       string              `json:"prompt"`
		Model        *string             `json:"model"`
		Capabilities *agent.Capabilities `json:"capabilities"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if namesNoSession(name, body.SessionID) {
		writeSessionIDRequired(w)
		return
	}

	ctx := r.Context()
	agentType := body.AgentType
	var storedSession *store.LiveSession
	if body.SessionID != "" {
		if ls, err := h.ss.GetLiveSession(ctx, body.SessionID); err == nil && ls != nil {
			if ls.Status != "active" || ls.StoppedAt != nil {
				writeJSON(w, http.StatusConflict, map[string]string{
					"error": "session is not live",
					"code":  "session_not_live",
				})
				return
			}
			if ls.IsArchived == 1 {
				writeJSON(w, http.StatusConflict, map[string]string{
					"error": "session is archived",
					"code":  "session_archived",
				})
				return
			}
			storedSession = ls
			// Restart requests from older clients may omit agent_type. Keep the
			// persisted provider instead of silently switching Codex to Claude.
			if agentType == "" && ls.AgentType != "" {
				agentType = ls.AgentType
			}
		}
	}
	if agentType == "" {
		agentType = at.Claude
	}

	// Sessions joined through the board API may have no board_name in the
	// session store. Resolve their subscription before renaming the terminal.
	var restartSubscription *board.Subscriber
	if h.bs != nil && body.SessionID != "" {
		oldAgentType := agentType
		if storedSession != nil {
			oldAgentType = storedSession.AgentType
		}
		var err error
		restartSubscription, err = h.bs.GetSubscriptionBySessionName(ctx, naming.SessionName(oldAgentType, body.SessionID))
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
	}

	// Validate the resolved model before touching the terminal or allocating a
	// replacement session. Restart carries the persisted model when the caller
	// omits one, so provider switches must reject stale cross-provider models
	// before any process or database state is mutated. When the caller explicitly
	// provides an empty model (""), they intend to clear the explicit override so
	// the agent falls back to its configured/default model.
	var preflightModel string
	if body.Model != nil {
		preflightModel = strings.TrimSpace(*body.Model)
	} else if storedSession != nil {
		preflightModel = strings.TrimSpace(derefStrPtr(storedSession.Model))
	}
	if err := validateAgentModel(agentType, preflightModel); err != nil {
		errBadRequest(w, err.Error())
		return
	}

	// A sleeping agent has no terminal. Restarting one starts a fresh agent in
	// a new terminal in its working directory, with the restart settings
	// (prompt, model, capabilities); waking would resume the old conversation.
	var sleeping *store.LiveSession
	if storedSession != nil && storedSession.IsSleeping == 1 {
		sleeping = storedSession
	}
	// An awake agent whose own terminal is gone (it crashed or was closed) restarts the
	// same way: a fresh terminal in its working directory, never another agent's pane
	// (#3213; before, the restart answered terminal_gone until the agent showed asleep).
	fresh := sleeping
	if fresh == nil && storedSession != nil && storedSession.WorkingDir != "" {
		if pane, err := h.terminal.FindSession(ctx, name, agentType, body.SessionID); err == nil && pane == nil {
			fresh = storedSession
		}
	}

	newSessionID := generateUUID()
	newSessionName := naming.SessionName(agentType, newSessionID)
	newLogPath := naming.LogFile(h.cfg.LogDir, agentType, newSessionID)

	// Find the terminal and build the launch command before anything is touched: when the
	// command can't be built (its prompt or settings files can't be written, #3601 W12), the
	// restart fails with the old agent still running.
	var workdir, target string
	usePTY := false
	var pane *ptymanager.PaneInfo
	if fresh != nil {
		workdir = fresh.WorkingDir
		usePTY = fresh.Backend != nil && *fresh.Backend == "pty" && h.backend != nil
		if _, onPTY := h.backend.(*ptymanager.PTYBackend); onPTY {
			usePTY = true // a pty Coral restarts every agent the pty way (#3634)
		}
	} else {
		var err error
		pane, err = h.terminal.FindSession(ctx, name, agentType, body.SessionID)
		if err == nil && pane == nil && body.SessionID != "" {
			writeTerminalError(w, ptymanager.ErrTerminalGone)
			return
		}
		if err != nil || pane == nil {
			errNotFound(w, "Pane not found")
			return
		}
		workdir = pane.CurrentPath
	}

	// Load stored config from the DB
	agentImpl := agent.GetAgent(agentType)
	var storedFlags []string
	var storedPrompt, storedBoard, storedBoardServer, storedDisplayName, storedBoardType, storedModel string
	var storedContextWindow int
	var storedCaps *agent.Capabilities
	var storedCapsJSON *string
	var storedTools []string
	var storedToolsJSON *string
	if body.SessionID != "" {
		if ls, err := h.ss.GetLiveSession(ctx, body.SessionID); err == nil && ls != nil {
			storedFlags = store.UnmarshalFlags(ls.Flags)
			storedPrompt = derefStrPtr(ls.Prompt)
			storedBoard = derefStrPtr(ls.BoardName)
			storedBoardServer = derefStrPtr(ls.BoardServer)
			storedDisplayName = derefStrPtr(ls.DisplayName)
			storedBoardType = derefStrPtr(ls.BoardType)
			storedModel = derefStrPtr(ls.Model)
			storedContextWindow = ls.ContextWindow
			storedCapsJSON = ls.Capabilities
			storedToolsJSON = ls.Tools
			storedTools = store.UnmarshalFlags(ls.Tools)
			if ls.Capabilities != nil && *ls.Capabilities != "" {
				storedCaps = &agent.Capabilities{}
				json.Unmarshal([]byte(*ls.Capabilities), storedCaps)
			}
		}
	}

	if restartSubscription != nil {
		storedBoard = restartSubscription.Project
		if storedDisplayName == "" {
			storedDisplayName = restartSubscription.JobTitle
		}
	}

	// Request body overrides stored values (user edited in modal)
	if body.Prompt != "" {
		storedPrompt = body.Prompt
	}
	if body.Model != nil {
		trimmedModel := strings.TrimSpace(*body.Model)
		if trimmedModel != "" {
			storedModel = trimmedModel
			storedContextWindow = proxy.LookupContextWindow(trimmedModel)
		} else {
			storedModel = ""
			storedContextWindow = 0
		}
	}
	if body.Capabilities != nil {
		storedCaps = body.Capabilities
		storedCapsJSON = store.MarshalCapabilities(body.Capabilities)
	}

	// When capabilities are available, strip agent-specific permission flags
	// from the stored flags — capabilities are the source of truth and
	// LaunchCommand will generate the correct flags for the target agent type.
	// This prevents flag mismatches when changing agent type (e.g. Codex --full-auto → Claude).
	cleanFlags := stripAgentPermissionFlags(storedFlags)
	// Also strip --model from old flags (we'll re-add from storedModel)
	var finalFlags []string
	for i := 0; i < len(cleanFlags); i++ {
		flag := cleanFlags[i]
		if flag == "--model" || flag == "-m" {
			i++ // skip the value
			continue
		}
		if strings.HasPrefix(flag, "--model=") || strings.HasPrefix(flag, "-m=") {
			continue
		}
		finalFlags = append(finalFlags, flag)
	}
	if storedModel != "" {
		finalFlags = append(finalFlags, "--model", storedModel)
	}
	allFlags := append(finalFlags, strings.Fields(body.ExtraFlags)...)

	role := naming.SubscriberID(storedDisplayName, agentType)

	userSettings, _ := h.ss.GetSettings(ctx)

	cmd, err := launchCommand(agentImpl, agent.LaunchParams{
		SessionID:       newSessionID,
		SessionName:     newSessionName,
		ProtocolPath:    h.protocolPath(),
		Flags:           allFlags,
		WorkingDir:      workdir,
		CLIPath:         agent.ResolveCLIPath(userSettings, agentType), // Settings > CLI Paths (#3205)
		BoardName:       storedBoard,
		BoardLabel:      h.boardLabel(storedBoard),
		Role:            role,
		Prompt:          storedPrompt,
		PromptOverrides: promptOverrides(userSettings),
		BoardType:       storedBoardType,
		Capabilities:    storedCaps,
		Tools:           storedTools,
		PermissionMode:  userSettings["default_permission_mode"],
		CoralDir:        h.cfg.CoralDir(),
		CoralHost:       h.cfg.AgentHost(),
		CoralPort:       h.cfg.Port,
	})
	if err != nil {
		errInternalServer(w, fmt.Sprintf("launch command failed: %v", err))
		return
	}

	// Now the terminal: a fresh one, or the old pane respawned and renamed.
	if fresh != nil {
		if usePTY {
			if err := h.backend.Spawn(newSessionName, agentType, workdir, newSessionID, "", 200, 50); err != nil {
				errInternalServer(w, fmt.Sprintf("pty spawn failed: %v", err))
				return
			}
		} else {
			ptymanager.ResetAgentLog(newLogPath) // 0600, never through a link (#3601, W17)
			if err := h.terminal.CreateSession(ctx, newSessionName, workdir); err != nil {
				errInternalServer(w, err.Error())
				return
			}
			h.terminal.StartLogging(ctx, newSessionName, newLogPath)
		}
		target = newSessionName + ".0"
	} else {
		// Close old pipe-pane, respawn, rename
		h.terminal.StopLogging(ctx, pane.Target)
		if err := h.terminal.RestartPane(ctx, pane.Target, pane.CurrentPath); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if err := h.terminal.RenameSession(ctx, pane.SessionName, newSessionName); err != nil {
			errInternalServer(w, err.Error())
			return
		}

		target = fmt.Sprintf("%s:0.0", newSessionName)
		time.Sleep(500 * time.Millisecond)

		// Clear scrollback, create log, setup pipe-pane
		h.terminal.ClearHistory(ctx, target)
		ptymanager.ResetAgentLog(newLogPath) // 0600, never through a link (#3601, W17)
		h.terminal.StartLogging(ctx, target, newLogPath)
	}

	// Set pane title using native tmux command (avoids shell echo)
	folderName := filepath.Base(strings.TrimRight(workdir, "/"))
	if !usePTY {
		h.terminal.SetPaneTitle(ctx, target, fmt.Sprintf("%s — %s", folderName, agentType))
	}

	log.Printf("[launch] restart session=%s cmd=%s", target, cmd)
	if usePTY {
		if pb, ok := h.backend.(*ptymanager.PTYBackend); ok && !pb.WaitReady(newSessionName, 5*time.Second) {
			log.Printf("[launch] pty shell not ready after 5s, sending anyway: %s", newSessionName)
		}
		// Route through the terminal abstraction so oversized commands use the
		// protected script handoff instead of hitting the shell's canonical
		// input limit while a fresh PTY is initializing.
		if err := sendLaunchCommand(ctx, h.terminal, newSessionName, cmd); err != nil {
			errInternalServer(w, fmt.Sprintf("pty launch command failed: %v", err))
			return
		}
	} else {
		if err := sendLaunchCommand(ctx, h.terminal, target, cmd); err != nil {
			errInternalServer(w, fmt.Sprintf("launch command failed: %v", err))
			return
		}
	}

	// Capture shell PID for process-tree-based identity resolution
	var restartPID int
	if tmuxTerm, ok := h.terminal.(*ptymanager.TmuxSessionTerminal); ok {
		if pid, err := tmuxTerm.Client().GetPanePID(ctx, newSessionName); err == nil {
			restartPID = pid
			log.Printf("[launch] restart captured PID %d for session %s", pid, newSessionName)
		} else {
			log.Printf("[launch] restart failed to capture PID for session %s: %v", newSessionName, err)
		}
	}

	// Prepare DB flags and model pointers. When storedModel is empty (either
	// explicitly cleared via empty/whitespace body.Model, or inherited as empty),
	// use a non-nil pointer to "" so ReplaceLiveSession preserves the cleared
	// state across subsequent restarts rather than carrying forward any prior model.
	// Similarly, serialize empty flags as "[]" so cleared flags are not reverted.
	var storedModelPtr *string
	if storedModel == "" {
		emptyModel := ""
		storedModelPtr = &emptyModel
	} else {
		storedModelPtr = strPtr(storedModel)
	}

	var storedFlagsJSON *string
	if len(allFlags) > 0 {
		storedFlagsJSON = store.MarshalFlags(allFlags)
	} else {
		emptyJSON := "[]"
		storedFlagsJSON = &emptyJSON
	}

	// Replace live session in DB (carry forward stored fields)
	h.chatMeta.forget(body.SessionID, newSessionID)
	h.ss.ReplaceLiveSession(ctx, body.SessionID, &store.LiveSession{
		SessionID:     newSessionID,
		AgentType:     agentType,
		AgentName:     folderName,
		WorkingDir:    workdir,
		ResumeFromID:  strPtr(body.SessionID),
		Flags:         storedFlagsJSON,
		Prompt:        strPtr(storedPrompt),
		BoardName:     strPtr(storedBoard),
		BoardServer:   strPtr(storedBoardServer),
		BoardType:     strPtr(storedBoardType),
		Capabilities:  storedCapsJSON,
		Model:         storedModelPtr,
		ContextWindow: storedContextWindow,
		Tools:         storedToolsJSON,
		PID:           restartPID,
	})
	h.ss.MigrateDisplayName(ctx, body.SessionID, newSessionID)
	h.ss.MigrateSessionTags(ctx, body.SessionID, newSessionID) // and its tags (#3216)
	if sleeping != nil {
		// The new row inherits the sleeping flag; this agent is running now.
		h.ss.SetSessionSleeping(ctx, newSessionID, false)
		if bn := derefStrPtr(sleeping.BoardName); bn != "" && h.boardHandler != nil {
			h.boardHandler.SetPaused(bn, false)
		}
	}

	// Re-subscribe to board if needed
	if restartSubscription != nil {
		sub := restartSubscription
		if _, err := h.bs.Subscribe(ctx, sub.Project, sub.SubscriberID, sub.JobTitle, newSessionName,
			sub.OriginServer, sub.ReceiveMode, sub.CanPeek == 1); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		writeBoardStateFile(newSessionName, sub.Project, sub.JobTitle, h.cfg)
	} else if storedBoard != "" {
		h.setupBoardAndPrompt(newSessionID, newSessionName, agentType, storedBoard, storedDisplayName)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "session_id": newSessionID, "session_name": newSessionName,
	})
}

// Resume restarts with --resume to continue a historical session.
// POST /api/sessions/live/{name}/resume
func (h *SessionsHandler) Resume(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	name := chi.URLParam(r, "name")
	var body struct {
		SessionID        string `json:"session_id"`
		AgentType        string `json:"agent_type"`
		CurrentSessionID string `json:"current_session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SessionID == "" {
		errBadRequest(w, "session_id is required")
		return
	}

	agentType := body.AgentType
	if agentType == "" {
		agentType = at.Claude
	}
	if !agent.IsResumeID(agentType, body.SessionID) {
		errBadRequest(w, "session_id is not a valid resume id") // #3582: it goes into file paths
		return
	}
	agentImpl := agent.GetAgent(agentType)
	if !agentImpl.SupportsResume() {
		errBadRequest(w, fmt.Sprintf("Resume not supported for %s", agentType))
		return
	}

	ctx := r.Context()

	targetSID := body.CurrentSessionID
	if targetSID == "" {
		if _, sid := logparse.ParseSessionName(name); sid != "" {
			targetSID = sid
		} else if _, err := uuid.Parse(name); err == nil {
			targetSID = name
		}
	}

	if namesNoSession(name, targetSID) {
		writeSessionIDRequired(w)
		return
	}

	ls, err := h.ss.GetLiveSession(ctx, targetSID)
	if err != nil {
		slog.Warn("failed to validate live session metadata", "session_id", targetSID, "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Agent metadata is temporarily unavailable; try again.",
			"code":  "agent_metadata_unavailable",
		})
		return
	}
	if ls == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "live session not found",
			"code":  "session_not_found",
		})
		return
	}
	if ls.Status != "active" || ls.StoppedAt != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "session is not live",
			"code":  "session_not_live",
		})
		return
	}
	if ls.IsArchived == 1 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "session is archived",
			"code":  "session_archived",
		})
		return
	}

	body.CurrentSessionID = targetSID

	// The agent's own terminal, or (#3213) when it is gone a fresh one in the agent's
	// working directory: never another agent's pane.
	var workdir string
	pane, _ := h.terminal.FindSession(ctx, name, agentType, targetSID)
	if pane == nil {
		if ls.WorkingDir != "" {
			workdir = ls.WorkingDir
		} else {
			writeTerminalError(w, ptymanager.ErrTerminalGone)
			return
		}
	} else {
		workdir = pane.CurrentPath
	}

	slog.Info("resuming agent", "agent_name", name, "session_id", body.SessionID, "agent_type", agentType)

	// Prepare resume files
	agent.TryPrepareResume(agentImpl, body.SessionID, workdir)

	newSessionID := generateUUID()
	newSessionName := naming.SessionName(agentType, newSessionID)
	newLogPath := naming.LogFile(h.cfg.LogDir, agentType, newSessionID)

	// Load stored fields from current session
	storedPrompt := derefStrPtr(ls.Prompt)
	storedSleeping := ls.IsSleeping == 1
	storedAgentType := agentType
	if ls.AgentType != "" {
		storedAgentType = ls.AgentType
	}
	storedBoard := derefStrPtr(ls.BoardName)
	storedBoardServer := derefStrPtr(ls.BoardServer)
	storedDisplayName := derefStrPtr(ls.DisplayName)
	storedBoardType := derefStrPtr(ls.BoardType)
	storedTools := store.UnmarshalFlags(ls.Tools)

	role := naming.SubscriberID(storedDisplayName, agentType)

	userSettings, _ := h.ss.GetSettings(ctx)

	// The launch command is built before the terminal is touched: when it can't be built
	// (#3601 W12), the resume fails with the old agent still running.
	cmd, err := launchCommand(agentImpl, agent.LaunchParams{
		SessionID:       newSessionID,
		SessionName:     newSessionName,
		ProtocolPath:    h.protocolPath(),
		ResumeSessionID: body.SessionID,
		WorkingDir:      workdir,
		CLIPath:         agent.ResolveCLIPath(userSettings, agentType), // Settings > CLI Paths (#3205)
		BoardName:       storedBoard,
		BoardLabel:      h.boardLabel(storedBoard),
		Role:            role,
		Prompt:          storedPrompt,
		PromptOverrides: promptOverrides(userSettings),
		BoardType:       storedBoardType,
		Tools:           storedTools,
		PermissionMode:  userSettings["default_permission_mode"],
		CoralDir:        h.cfg.CoralDir(),
		CoralHost:       h.cfg.AgentHost(),
		CoralPort:       h.cfg.Port,
	})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	target := fmt.Sprintf("%s:0.0", newSessionName)
	if pane == nil {
		ptymanager.ResetAgentLog(newLogPath) // 0600, never through a link (#3601, W17)
		if err := h.terminal.CreateSession(ctx, newSessionName, workdir); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		target = newSessionName + ".0"
		h.terminal.StartLogging(ctx, newSessionName, newLogPath)
	} else {
		h.terminal.StopLogging(ctx, pane.Target)
		h.terminal.RestartPane(ctx, pane.Target, pane.CurrentPath)
		h.terminal.RenameSession(ctx, pane.SessionName, newSessionName)
		time.Sleep(500 * time.Millisecond)
		h.terminal.ClearHistory(ctx, target)
		ptymanager.ResetAgentLog(newLogPath) // 0600, never through a link (#3601, W17)
		h.terminal.StartLogging(ctx, target, newLogPath)
	}
	if err := sendLaunchCommand(ctx, h.terminal, target, cmd); err != nil {
		errInternalServer(w, err.Error())
		return
	}

	// Capture shell PID
	var resumePID int
	if tmuxTerm, ok := h.terminal.(*ptymanager.TmuxSessionTerminal); ok {
		if pid, err := tmuxTerm.Client().GetPanePID(ctx, newSessionName); err == nil {
			resumePID = pid
		}
	}

	// The agent's board subscription (job title, receive mode, peek), carried to
	// the new session as restart does (#3216).
	var resumeSubscription *board.Subscriber
	if h.bs != nil && body.CurrentSessionID != "" {
		resumeSubscription, _ = h.bs.GetSubscriptionBySessionName(ctx, naming.SessionName(storedAgentType, body.CurrentSessionID))
	}

	h.chatMeta.forget(body.CurrentSessionID, newSessionID)
	h.ss.ReplaceLiveSession(ctx, body.CurrentSessionID, &store.LiveSession{
		SessionID:    newSessionID,
		AgentType:    agentType,
		AgentName:    filepath.Base(strings.TrimRight(workdir, "/")),
		WorkingDir:   workdir,
		ResumeFromID: strPtr(body.SessionID),
		Prompt:       strPtr(storedPrompt),
		BoardName:    strPtr(storedBoard),
		BoardServer:  strPtr(storedBoardServer),
		BoardType:    strPtr(storedBoardType),
		PID:          resumePID,
	})

	// The agent keeps its name and tags, like a restart (#3216).
	if body.CurrentSessionID != "" {
		h.ss.MigrateDisplayName(ctx, body.CurrentSessionID, newSessionID)
		h.ss.MigrateSessionTags(ctx, body.CurrentSessionID, newSessionID)
	}
	// The new row inherits the sleeping flag; the resumed agent is running now, as
	// after a restart. Left asleep, team wake and the board notifier skipped it
	// until the server restarted (#3273).
	h.ss.SetSessionSleeping(ctx, newSessionID, false)
	if storedSleeping && storedBoard != "" && h.boardHandler != nil {
		h.boardHandler.SetPaused(storedBoard, false)
	}

	// Re-subscribe to board — subscriber_id (role) is stable, so the cursor persists automatically.
	if sub := resumeSubscription; sub != nil {
		if _, err := h.bs.Subscribe(ctx, sub.Project, sub.SubscriberID, sub.JobTitle, newSessionName,
			sub.OriginServer, sub.ReceiveMode, sub.CanPeek == 1); err != nil {
			log.Printf("[resume] re-subscribe %s to board %s: %v", newSessionName, sub.Project, err)
		}
		writeBoardStateFile(newSessionName, sub.Project, sub.JobTitle, h.cfg)
	} else if storedBoard != "" {
		h.setupBoardAndPrompt(newSessionID, newSessionName, agentType, storedBoard, storedDisplayName)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "session_id": newSessionID, "session_name": newSessionName,
	})
}

// Attach opens a native terminal attached to the tmux session.
// POST /api/sessions/live/{name}/attach
func (h *SessionsHandler) Attach(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		AgentType string `json:"agent_type"`
		SessionID string `json:"session_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if namesNoSession(name, body.SessionID) {
		writeSessionIDRequired(w)
		return
	}

	pane, _ := h.terminal.FindSession(r.Context(), name, body.AgentType, body.SessionID)
	if pane == nil {
		errNotFound(w, "Pane not found")
		return
	}

	// Open Terminal.app attached to the tmux session (macOS)
	attachCmd := h.terminal.AttachCommand(pane.SessionName)
	go func() {
		exec.Command("osascript", terminalAttachArgs(attachCmd)...).Run()
	}()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Pass the shell command as data, so quotes and backslashes in app paths never
// become AppleScript source.
func terminalAttachArgs(command string) []string {
	return []string{"-e", `on run argv
    tell application "Terminal" to do script (item 1 of argv)
end run`, "--", command}
}

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

// Launch creates a new agent session.
// POST /api/sessions/launch
func (h *SessionsHandler) Launch(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	var body struct {
		WorkingDir      string                 `json:"working_dir"`
		AgentType       string                 `json:"agent_type"`
		DisplayName     string                 `json:"display_name"`
		Flags           []string               `json:"flags"`
		Prompt          string                 `json:"prompt"`
		BoardName       string                 `json:"board_name"`
		BoardID         string                 `json:"board_id"`  // join this board (#3536)
		NewBoard        bool                   `json:"new_board"` // create another board even if the name exists
		BoardServer     string                 `json:"board_server"`
		Backend         string                 `json:"backend"`
		BoardType       string                 `json:"board_type"`
		Model           string                 `json:"model"`
		ResumeSessionID string                 `json:"resume_session_id"`
		Capabilities    *agent.Capabilities    `json:"capabilities"`
		Tools           []string               `json:"tools"`
		MCPServers      json.RawMessage        `json:"mcpServers"` // refused: not supported (#3644)
		Hooks           map[string]interface{} `json:"hooks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if mcpGiven(body.MCPServers) {
		errBadRequest(w, errMCPNotSupported)
		return
	}
	// No agent_type (e.g. `coral-agent launch`) means the operator's default,
	// as the + New Agent modal pre-selects it.
	if body.AgentType == "" {
		if settings, err := h.ss.GetSettings(r.Context()); err == nil {
			body.AgentType = settings["default_agent_type"]
		}
	}
	if body.ResumeSessionID != "" && !agent.IsResumeID(body.AgentType, body.ResumeSessionID) {
		errBadRequest(w, "resume_session_id is not a valid resume id") // #3582: it goes into file paths
		return
	}
	// Analytics: one requested/result pair per attempt, with controlled
	// categories only (never error text, paths, names or prompts).
	attempt := tracking.StartLaunch(tracking.KindAgent, tracking.ProviderFor(body.AgentType), h.launchBackendLabel(body.Backend), 1, body.ResumeSessionID != "")
	if body.WorkingDir == "" {
		attempt.Finish(tracking.OutcomeFailure, tracking.FailInvalidRequest, 0, 1)
		errBadRequest(w, "working_dir is required")
		return
	}
	// Which board on this Coral the agent joins or starts (#3536); a remote board's Coral decides its own.
	if (body.BoardName != "" || body.BoardID != "") && (body.BoardServer == "" || body.BoardServer == "local") {
		key, ok := h.launchBoard(w, r, body.BoardID, body.BoardName, body.NewBoard)
		if !ok {
			attempt.Finish(tracking.OutcomeFailure, tracking.FailInvalidRequest, 0, 1)
			return
		}
		body.BoardName = key
	}
	// An unknown agent_type used to fall through to Claude and return 200
	// ok:true, silently starting an agent the caller never asked for.
	if err := agent.ValidateAgentType(body.AgentType); err != nil {
		attempt.Finish(tracking.OutcomeFailure, tracking.FailInvalidRequest, 0, 1)
		errBadRequest(w, err.Error())
		return
	}
	// A directory that does not exist is the caller's mistake: 400 naming the
	// field, so the dialog can mark it (it used to surface as a 500) (#3152).
	if msg := launchDirError(body.WorkingDir); msg != "" {
		attempt.Finish(tracking.OutcomeFailure, tracking.FailWorkdirUnavailable, 0, 1)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg, "field": "working_dir"})
		return
	}
	slog.Info("launch request received",
		"working_dir", body.WorkingDir,
		"agent_type", body.AgentType,
		"display_name", body.DisplayName,
		"requested_backend", body.Backend,
		"terminal_type", fmt.Sprintf("%T", h.terminal),
		"backend_type", fmt.Sprintf("%T", h.backend),
		"path", os.Getenv("PATH"),
		"shell", os.Getenv("SHELL"),
		"coral_tmux_bin", os.Getenv("CORAL_TMUX_BIN"),
	)

	// Validate resume support
	if body.ResumeSessionID != "" {
		agentImpl := agent.GetAgent(body.AgentType)
		if agentImpl != nil && !agentImpl.SupportsResume() {
			attempt.Finish(tracking.OutcomeFailure, tracking.FailUnsupportedOption, 0, 1)
			errBadRequest(w, fmt.Sprintf("Resume not supported for %s", body.AgentType))
			return
		}
	}

	// Resolve model: request wins, else per-type default from settings.
	effectiveModel := h.resolveModel(r.Context(), body.AgentType, body.Model)

	// Add model flag if resolved to non-empty
	launchFlags := body.Flags
	if effectiveModel != "" {
		launchFlags = append(append([]string{}, body.Flags...), "--model", effectiveModel)
	}
	result, err := h.launchSession(r.Context(), body.WorkingDir, body.AgentType, body.DisplayName,
		body.ResumeSessionID, launchFlags, body.Prompt, body.BoardName, body.BoardServer, body.Backend, body.BoardType, effectiveModel, body.Capabilities,
		body.Tools, body.Hooks)
	if err != nil {
		attempt.Finish(tracking.OutcomeFailure, classifyLaunchError(err), 0, 1)
		errInternalServer(w, err.Error())
		return
	}
	attempt.Finish(tracking.OutcomeSuccess, "", 1, 0)

	// Setup board subscription in background (prompt is passed as CLI arg, not via tmux send-keys)
	if body.BoardName != "" {
		h.setupBoardAndPrompt(result["session_id"].(string), result["session_name"].(string),
			body.AgentType, body.BoardName, launchedName(result, body.DisplayName))
	}

	tracking.TrackEvent(tracking.EventSessionLaunched, nil)
	tracking.TrackOnce(tracking.EventFirstAgentLaunched, nil)
	if body.BoardName != "" && (body.BoardServer == "" || body.BoardServer == "local") {
		result["board_id"] = body.BoardName
		result["board_label"] = h.boardLabel(body.BoardName)
	}
	writeJSON(w, http.StatusOK, result)
}

// LaunchTeam launches multiple agents on a shared message board.
// POST /api/sessions/launch-team
func (h *SessionsHandler) LaunchTeam(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	var body struct {
		BoardName   string   `json:"board_name"`
		BoardID     string   `json:"board_id"`  // relaunch on this board (#3536)
		NewBoard    bool     `json:"new_board"` // create another board even if the name exists
		WorkingDir  string   `json:"working_dir"`
		AgentType   string   `json:"agent_type"`
		Flags       []string `json:"flags"`
		BoardServer string   `json:"board_server"`
		Backend     string   `json:"backend"`
		BoardType   string   `json:"board_type"`
		Worktree    bool     `json:"worktree"`
		BaseBranch  string   `json:"base_branch"`
		Agents      []struct {
			Name         string                 `json:"name"`
			Prompt       string                 `json:"prompt"`
			Capabilities *agent.Capabilities    `json:"capabilities"`
			AgentType    string                 `json:"agent_type"`
			Model        string                 `json:"model"`
			Flags        []string               `json:"flags"`
			Tools        []string               `json:"tools"`
			MCPServers   json.RawMessage        `json:"mcpServers"` // refused: not supported (#3644)
			Hooks        map[string]interface{} `json:"hooks"`
			Knowledge    string                 `json:"knowledge"`
		} `json:"agents"`
		TeamKnowledgeIndex string `json:"team_knowledge_index"`
		WorkingMode        *struct {
			Mode               string `json:"mode"`
			DependencyGuidance bool   `json:"dependency_guidance"`
			CustomInstructions string `json:"custom_instructions"`
		} `json:"working_mode"`
		WorkflowPresets []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Instructions string `json:"instructions"`
			Builtin      bool   `json:"builtin"`
		} `json:"workflow_presets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	for _, a := range body.Agents {
		if mcpGiven(a.MCPServers) {
			errBadRequest(w, fmt.Sprintf("agent %q: %s", a.Name, errMCPNotSupported))
			return
		}
	}
	// Which board the team joins or starts (#3536), decided before anything is written
	// (knowledge files, working mode, presets, worktrees, the teams row). From here on
	// body.BoardName is the board's id.
	// The base branch reaches git: never an option, and checked before anything is created
	// (#3601, weak site W24).
	if body.Worktree && body.BaseBranch != "" {
		if err := gitutil.ValidRef(body.BaseBranch); err != nil {
			errBadRequest(w, "base_branch: "+err.Error())
			return
		}
	}
	if body.BoardName != "" || body.BoardID != "" {
		key, ok := h.launchBoard(w, r, body.BoardID, body.BoardName, body.NewBoard)
		if !ok {
			return
		}
		body.BoardName = key
	}

	// Write any bundled knowledge files from the template
	if body.BoardName != "" {
		coralDir := h.cfg.CoralDir()
		hasKnowledge := false
		for _, a := range body.Agents {
			if a.Knowledge != "" {
				background.WriteKnowledgeFile(coralDir, body.BoardName, a.Name, a.Knowledge)
				hasKnowledge = true
			}
		}
		if body.TeamKnowledgeIndex != "" {
			background.WriteKnowledgeIndex(coralDir, body.BoardName, body.TeamKnowledgeIndex)
			hasKnowledge = true
		}
		if hasKnowledge {
			log.Printf("[launch-team] restored %d agent knowledge files for team %s", len(body.Agents), body.BoardName)
		}
	}

	// Restore working mode settings from template
	if body.WorkingMode != nil && h.bs != nil {
		wm := board.WorkingMode{
			Mode:               body.WorkingMode.Mode,
			DependencyGuidance: body.WorkingMode.DependencyGuidance,
			CustomInstructions: body.WorkingMode.CustomInstructions,
		}
		if _, err := h.bs.SetWorkingMode(r.Context(), body.BoardName, wm); err != nil {
			log.Printf("[launch-team] failed to restore working mode for team %s: %v", body.BoardName, err)
		} else {
			log.Printf("[launch-team] restored working mode %q for team %s", wm.Mode, body.BoardName)
		}
	}

	// Restore workflow presets from template
	if len(body.WorkflowPresets) > 0 && h.bs != nil {
		for _, p := range body.WorkflowPresets {
			if p.Builtin {
				h.bs.SaveWorkflowPreset(r.Context(), body.BoardName, p.ID, p.Name, p.Instructions, false)
			} else {
				h.bs.SaveWorkflowPreset(r.Context(), body.BoardName, p.ID, p.Name, p.Instructions, true)
			}
		}
		log.Printf("[launch-team] restored %d workflow presets for team %s", len(body.WorkflowPresets), body.BoardName)
	}

	// Label the team by the effective provider of the members that will
	// actually launch (their own type, else the team's, else the Claude
	// fallback launchSession applies): "mixed" only when those truly differ.
	var memberTypes []string
	skippedMembers := 0
	for _, a := range body.Agents {
		if a.Name == "" {
			skippedMembers++ // the launch loop skips unnamed members
			continue
		}
		t := body.AgentType
		if a.AgentType != "" {
			t = a.AgentType
		}
		memberTypes = append(memberTypes, t)
	}
	teamProvider := tracking.TeamProvider(memberTypes)
	attempt := tracking.StartLaunch(tracking.KindTeam, teamProvider, h.launchBackendLabel(body.Backend), len(body.Agents), false)
	if body.BoardName == "" || body.WorkingDir == "" || len(body.Agents) == 0 {
		attempt.Finish(tracking.OutcomeFailure, tracking.FailInvalidRequest, 0, len(body.Agents))
		errBadRequest(w, "board_name, working_dir, and agents required")
		return
	}
	// Validate the team-level type and every per-agent override before
	// launching anything: a team that fails halfway leaves live agents behind.
	if err := agent.ValidateAgentType(body.AgentType); err != nil {
		attempt.Finish(tracking.OutcomeFailure, tracking.FailInvalidRequest, 0, len(body.Agents))
		errBadRequest(w, err.Error())
		return
	}
	for _, a := range body.Agents {
		if err := agent.ValidateAgentType(a.AgentType); err != nil {
			attempt.Finish(tracking.OutcomeFailure, tracking.FailInvalidRequest, 0, len(body.Agents))
			errBadRequest(w, fmt.Sprintf("agent %q: %s", a.Name, err))
			return
		}
	}

	ctx := r.Context()

	// Create git worktree if requested
	workingDir := body.WorkingDir
	var worktreePath string
	if body.Worktree && body.WorkingDir != "" {
		branch := body.BaseBranch
		if branch == "" {
			branch = "main"
		}
		worktreeDir := filepath.Join(h.cfg.CoralDir(), "worktrees")
		os.MkdirAll(worktreeDir, 0755)
		worktreePath = filepath.Join(worktreeDir, body.BoardName)
		worktreeBranch := fmt.Sprintf("coral-team/%s", body.BoardName)
		cmd := gitutil.Command(ctx, body.WorkingDir, "worktree", "add", "-b", worktreeBranch, "--", worktreePath, branch)
		if out, err := cmd.CombinedOutput(); err != nil {
			// Branch may already exist from a previous run — try without -b
			cmd2 := gitutil.Command(ctx, body.WorkingDir, "worktree", "add", "--", worktreePath, worktreeBranch)
			if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
				attempt.Finish(tracking.OutcomeFailure, tracking.FailWorktreeFailed, 0, len(body.Agents))
				errBadRequest(w, fmt.Sprintf("git worktree add failed: %s / %s", string(out), string(out2)))
				return
			}
		}
		workingDir = worktreePath
		log.Printf("[launch-team] created worktree at %s (branch %s from %s)", worktreePath, worktreeBranch, branch)
	}

	// Create team persistence record
	var teamID int64
	if h.teamStore != nil {
		configJSON, _ := json.Marshal(body)
		isWorktree := 0
		if worktreePath != "" {
			isWorktree = 1
		}
		team, err := h.teamStore.CreateTeam(ctx, &store.Team{
			Name:       body.BoardName,
			ConfigJSON: string(configJSON),
			Status:     "running",
			WorkingDir: workingDir,
			IsWorktree: isWorktree,
		})
		if err != nil {
			log.Printf("[launch-team] failed to create team record: %v", err)
		} else {
			teamID = team.ID
		}
	}

	var launched []map[string]any
	firstFailure := ""

	// Load settings once so we can fall back to per-type default_model_<type>.
	userSettings, _ := h.ss.GetSettings(ctx)

	for _, agentDef := range body.Agents {
		if agentDef.Name == "" {
			continue
		}

		// Per-agent type/model override team-level defaults
		agentType := body.AgentType
		if agentDef.AgentType != "" {
			agentType = agentDef.AgentType
		}

		// Resolve model: per-agent request wins, else user's default_model_<type>.
		effectiveModel := defaultModelFromSettings(userSettings, agentType, agentDef.Model)

		// Per-agent flags carry the member's own permission mode from the UI.
		// When present, use them (after stripping engine-specific permission
		// flags that might not match this agent's type — launchSession will
		// re-derive the correct flags from the cleaned set). Otherwise fall
		// back to team-level flags (also cleaned).
		var agentFlags []string
		if len(agentDef.Flags) > 0 {
			agentFlags = agentDef.Flags
		} else {
			agentFlags = stripAgentPermissionFlags(body.Flags)
		}
		if effectiveModel != "" {
			agentFlags = append(agentFlags, "--model", effectiveModel)
		}

		// Inject distilled knowledge if available
		agentPrompt := InjectKnowledgeIntoPrompt(h.cfg.CoralDir(), body.BoardName, agentDef.Name, agentDef.Prompt)

		result, err := h.launchSession(ctx, workingDir, agentType, agentDef.Name,
			"", agentFlags, agentPrompt, body.BoardName, body.BoardServer, body.Backend, body.BoardType, effectiveModel, agentDef.Capabilities,
			agentDef.Tools, agentDef.Hooks)
		if err != nil {
			log.Printf("[launch-team] failed to launch agent %s: %v", agentDef.Name, err)
			if firstFailure == "" {
				firstFailure = classifyLaunchError(err)
			}
			launched = append(launched, map[string]any{"name": agentDef.Name, "error": err.Error()})
			continue
		}

		sessionID := result["session_id"].(string)

		// Store worktree info on the live session
		if worktreePath != "" {
			h.ss.UpdateWorktreeInfo(ctx, sessionID, worktreePath, body.WorkingDir)
		}

		// Create team member record and set team_id on live session
		if teamID > 0 && h.teamStore != nil {
			agentConfigJSON, _ := json.Marshal(agentDef)
			h.teamStore.CreateTeamMember(ctx, &store.TeamMember{
				TeamID:          teamID,
				AgentName:       agentDef.Name,
				AgentConfigJSON: string(agentConfigJSON),
				SessionID:       &sessionID,
				Status:          "active",
			})
			h.ss.SetTeamID(ctx, sessionID, teamID)
		}

		// Board subscription handled by setupBoardAndPrompt (prompt passed as CLI arg)
		if body.BoardName != "" {
			h.setupBoardAndPrompt(sessionID, result["session_name"].(string),
				agentType, body.BoardName, launchedName(result, agentDef.Name))
		}

		launchResult := map[string]any{
			"name": launchedName(result, agentDef.Name), "session_id": sessionID, "session_name": result["session_name"],
		}
		if worktreePath != "" {
			launchResult["worktree_path"] = worktreePath
		}
		launched = append(launched, launchResult)
	}

	resp := map[string]any{"ok": true, "board": body.BoardName, "board_id": body.BoardName, "board_label": h.boardLabel(body.BoardName), "agents": launched}
	if teamID > 0 {
		resp["team_id"] = teamID
	}
	// launched holds both started members and {name, error} failures, so count
	// each kind separately: only started members are real launches.
	started, failed := 0, 0
	for _, m := range launched {
		if _, bad := m["error"]; bad {
			failed++
		} else {
			started++
		}
	}
	// Unnamed members were never launched: count them as failed (not started)
	// with a fixed category so requested == started + failed.
	failed += skippedMembers
	if skippedMembers > 0 && firstFailure == "" {
		firstFailure = tracking.FailInvalidRequest
	}
	switch {
	case failed == 0 && started > 0:
		attempt.Finish(tracking.OutcomeSuccess, "", started, 0)
	case started == 0:
		attempt.Finish(tracking.OutcomeFailure, firstFailure, 0, failed)
	default:
		attempt.Finish(tracking.OutcomePartial, firstFailure, started, failed)
	}
	if started > 0 {
		counts := map[string]string{
			"agent_count": strconv.Itoa(started), "requested_agents": strconv.Itoa(len(body.Agents)),
			"started_agents": strconv.Itoa(started), "failed_agents": strconv.Itoa(failed),
		}
		tracking.TrackEvent(tracking.EventTeamLaunched, counts)
		tracking.TrackOnce(tracking.EventFirstTeamLaunched, map[string]string{"agent_count": strconv.Itoa(started)})
	}
	writeJSON(w, http.StatusOK, resp)
}

// ResetTeam kills all agents on a board and re-launches them with their
// original prompts and configuration. Each agent gets a fresh context.
// POST /api/sessions/live/team/{boardName}/reset
func (h *SessionsHandler) ResetTeam(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	boardName := urlParam(r, "boardName")
	if boardName == "" {
		errBadRequest(w, "boardName is required")
		return
	}

	ctx := r.Context()
	bgCtx := context.Background()

	// Get all sessions on this board
	sessions, err := h.ss.GetBoardSessions(ctx, boardName)
	if err != nil {
		errInternalServer(w, "failed to get board sessions: "+err.Error())
		return
	}
	if len(sessions) == 0 {
		errBadRequest(w, "no agents found on board: "+boardName)
		return
	}

	// Save each agent's config before killing
	type agentConfig struct {
		DisplayName  *string
		WorkingDir   string
		AgentType    string
		Flags        *string
		Prompt       *string
		BoardServer  *string
		BoardType    *string
		Icon         *string
		Capabilities *string
		Model        *string
		Tools        *string
	}
	configs := make([]agentConfig, 0, len(sessions))
	for _, s := range sessions {
		dn := s.DisplayName
		// Fallback: if display_name is nil in live_sessions, check session_meta
		if dn == nil {
			if metaName, err := h.ss.GetDisplayName(ctx, s.SessionID); err == nil && metaName != nil {
				dn = metaName
			}
		}
		configs = append(configs, agentConfig{
			DisplayName:  dn,
			WorkingDir:   s.WorkingDir,
			AgentType:    s.AgentType,
			Flags:        s.Flags,
			Prompt:       s.Prompt,
			BoardServer:  s.BoardServer,
			BoardType:    s.BoardType,
			Icon:         s.Icon,
			Capabilities: s.Capabilities,
			Model:        s.Model,
			Tools:        s.Tools,
		})
	}

	// Kill all agents and unsubscribe from board
	for _, s := range sessions {
		h.chatMeta.forget(s.SessionID)
		h.ss.UnregisterLiveSession(bgCtx, s.SessionID)
		h.terminal.KillSession(bgCtx, s.AgentName, s.AgentType, s.SessionID)
		removeBoardStateFile(s.AgentName, h.cfg)
		// Mark subscriber as inactive during reset; re-launch will reactivate.
		if h.bs != nil && s.DisplayName != nil && *s.DisplayName != "" {
			h.bs.Unsubscribe(bgCtx, boardName, *s.DisplayName)
		}
	}

	// Clear board pause state
	if h.boardHandler != nil {
		h.boardHandler.SetPaused(boardName, false)
	}

	// Brief pause to let tmux sessions clean up
	time.Sleep(500 * time.Millisecond)

	// Re-launch each agent with original config
	var launched []map[string]any
	var relaunched []string // the names the agents were launched under
	for _, cfg := range configs {
		var flags []string
		if cfg.Flags != nil && *cfg.Flags != "" {
			json.Unmarshal([]byte(*cfg.Flags), &flags)
		}
		prompt := ""
		if cfg.Prompt != nil {
			prompt = *cfg.Prompt
		}
		boardServer := ""
		if cfg.BoardServer != nil {
			boardServer = *cfg.BoardServer
		}
		boardType := ""
		if cfg.BoardType != nil {
			boardType = *cfg.BoardType
		}
		displayName := ""
		if cfg.DisplayName != nil {
			displayName = *cfg.DisplayName
		}

		// Restore model and capabilities from saved config
		modelStr := ""
		if cfg.Model != nil {
			modelStr = *cfg.Model
		}
		var caps *agent.Capabilities
		if cfg.Capabilities != nil && *cfg.Capabilities != "" {
			caps = &agent.Capabilities{}
			json.Unmarshal([]byte(*cfg.Capabilities), caps)
		}
		// Add model flag if stored
		if modelStr != "" {
			flags = append(flags, "--model", modelStr)
		}
		tools := store.UnmarshalFlags(cfg.Tools)
		prompt = InjectKnowledgeIntoPrompt(h.cfg.CoralDir(), boardName, displayName, prompt)
		result, err := h.launchSession(bgCtx, cfg.WorkingDir, cfg.AgentType, displayName,
			"", flags, prompt, boardName, boardServer, "", boardType, modelStr, caps,
			tools, nil)
		if err != nil {
			log.Printf("[reset-team] failed to re-launch %s: %v", displayName, err)
			launched = append(launched, map[string]any{"name": displayName, "error": err.Error()})
			continue
		}

		// Re-setup board subscription
		h.setupBoardAndPrompt(result["session_id"].(string), result["session_name"].(string),
			cfg.AgentType, boardName, launchedName(result, displayName))

		name := launchedName(result, displayName)
		if name != "" {
			relaunched = append(relaunched, name)
		}
		launched = append(launched, map[string]any{
			"name":         name,
			"session_id":   result["session_id"],
			"session_name": result["session_name"],
		})
	}

	// Advance read cursors for ALL agents AFTER all launches complete.
	// This ensures no agent has a stale cursor from messages posted by
	// other agents during the sequential re-launch process. By the name each was launched
	// under: a numbered one ("Backend Dev 2", #3543) has its own subscription (#3549).
	if h.bs != nil {
		for _, name := range relaunched {
			if err := h.bs.AdvanceReadCursor(bgCtx, boardName, name); err != nil {
				log.Printf("[reset-team] failed to advance read cursor for %s: %v", name, err)
			}
		}
	}

	log.Printf("[reset-team] reset board %q: %d agents", boardName, len(launched))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "board": boardName, "agents": launched})
}

// ── Tasks ───────────────────────────────────────────────────────────────

func (h *SessionsHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)
	tasks, err := h.ts.ListAgentTasks(r.Context(), name, sidPtr)
	if err != nil || tasks == nil {
		tasks = []store.AgentTask{}
	}
	// A personal task belongs to its exact session, not the workspace/folder
	// name. Resolve the current or persisted display name by session ID so a
	// team task table cannot label every personal row with the workspace name.
	if sidPtr != nil && *sidPtr != "" {
		if display, ok := h.sessionDisplayName(r.Context(), *sidPtr); ok {
			for i := range tasks {
				tasks[i].DisplayName = &display
			}
		}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *SessionsHandler) sessionDisplayName(ctx context.Context, sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	if live, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && live != nil && live.DisplayName != nil && strings.TrimSpace(*live.DisplayName) != "" {
		return strings.TrimSpace(*live.DisplayName), true
	}
	if saved, err := h.ss.GetDisplayName(ctx, sessionID); err == nil && saved != nil && strings.TrimSpace(*saved) != "" {
		return strings.TrimSpace(*saved), true
	}
	return "", false
}

func (h *SessionsHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title     string             `json:"title"`
		Body      string             `json:"body"`
		Priority  string             `json:"priority"`
		SessionID string             `json:"session_id"`
		Workflow  board.TaskWorkflow `json:"workflow"`
		BlockedBy json.RawMessage    `json:"blocked_by"`
		Draft     bool               `json:"draft"`
		// Notify tells the agent to claim it (a short prompt typed into its
		// terminal; see claimPrompt).
		Notify bool `json:"notify"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "title is required")
		return
	}
	// A title is one line (it is shown in lists and prompts)
	body.Title = oneLineTitle(body.Title)
	if body.Title == "" {
		errBadRequest(w, "title is required")
		return
	}
	// Whose list it goes on (#3498): an agent's own, or the path's for the Operator.
	// A notify types a claim prompt into that agent, so the path can't redirect it.
	claim := body.SessionID
	if claim == "" {
		claim = queryClaim(r)
	}
	scope, ok := h.itemScope(w, r, claim)
	if !ok {
		return
	}
	name := scope.name
	if scope.sid != nil {
		body.SessionID = *scope.sid
	}

	// One row per open task: the operator's task and the agent's own
	// TaskCreate for it (synced by title) converge instead of duplicating.
	if existing, err := h.ts.FindOpenAgentTask(r.Context(), name, body.Title, strPtr(body.SessionID)); err == nil && existing != nil {
		writeJSON(w, http.StatusOK, existing)
		return
	}

	// Resolve display_name from live session
	var displayName *string
	var dn string
	err := h.db.Reader().GetContext(r.Context(), &dn,
		"SELECT display_name FROM live_sessions WHERE agent_name = ? AND status = 'active' AND display_name IS NOT NULL LIMIT 1", name)
	if err == nil && dn != "" {
		displayName = &dn
	}

	var deps []board.TaskDep
	if len(body.BlockedBy) > 0 {
		deps, err = parseBlockedBy(body.BlockedBy, store.AgentTaskProject(name, strPtr(body.SessionID)))
		if err != nil {
			errBadRequest(w, err.Error())
			return
		}
	}
	task, err := h.ts.CreateAgentTaskWithWorkflow(r.Context(), name, body.Title, strPtr(body.SessionID), displayName, body.Body, body.Priority, &board.CreateTaskOpts{Workflow: body.Workflow, BlockedBy: deps, Draft: body.Draft, MaxDepth: 32})
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	resp := createdTaskResponse{AgentTask: task}
	if body.Notify && task.Status == "pending" {
		resp.Notified, resp.NotifyError = h.notifyTaskCreated(r.Context(), name, body.SessionID, task)
	}
	writeJSON(w, http.StatusOK, resp)
}

// itemScope is whose tasks and notes a live-session route may create or change
// (#3498, security sweep item 9). claimed is the session the request names (its
// ?session_id=, or a create's body session_id).
//   - An agent, by its session token, acts only on its own: its agent name and session
//     come from the token, whatever the path says (the path's name comes from the
//     hook's working folder, which can drift: #3495's rule); claiming another session
//     is 403.
//   - A terminal session with no token (default mode only, the operator's rule as in
//     #3495) acts as the session it names, under that session's own agent name.
//   - The Operator (the dashboard, the verified CLI, the Hub's stamp) acts on the
//     path's {name}, and on the claimed session when one is given.
//   - Anyone else is 401 (callerSession).
//
// The GET list routes are not identity-bound (reads; #3475's design).
type itemScope struct {
	name  string
	sid   *string
	agent bool // an agent (or a terminal session) acting for itself
}

func (h *SessionsHandler) itemScope(w http.ResponseWriter, r *http.Request, claimed string) (itemScope, bool) {
	sid, ok := callerSession(w, r, claimed, true, h.isTerminalSession(r.Context()))
	if !ok {
		return itemScope{}, false
	}
	agentName := func(sessionID, fallback string) string {
		if ls, err := h.ss.GetLiveSession(r.Context(), sessionID); err == nil && ls != nil {
			return ls.AgentName
		}
		return fallback
	}
	if owner, isAgent := VerifiedSession(r.Context()); isAgent {
		return itemScope{name: agentName(owner.SessionID, owner.SessionName), sid: &sid, agent: true}, true
	}
	if !humanInputCaller(r) { // a terminal session, by the session it names
		return itemScope{name: agentName(sid, chi.URLParam(r, "name")), sid: &sid, agent: true}, true
	}
	return itemScope{name: chi.URLParam(r, "name"), sid: strPtr(claimed)}, true
}

// queryClaim is the session a request names in its query, or "".
func queryClaim(r *http.Request) string {
	return r.URL.Query().Get("session_id")
}

// owns reports whether an item of agent itemName and session itemSID is in scope: the
// same agent, and the same session when one is known (the list routes' rule). An item
// with no session (from before sessions were recorded) is its agent's.
func (s itemScope) owns(itemName string, itemSID *string) bool {
	if itemName != s.name {
		return false
	}
	if s.sid == nil {
		return true
	}
	if itemSID == nil {
		return s.agent
	}
	return *itemSID == *s.sid
}

// ownTask returns the task, or answers 404 when it isn't in scope.
func (h *SessionsHandler) ownTask(w http.ResponseWriter, r *http.Request, scope itemScope, taskID int64) (*store.AgentTask, bool) {
	t, err := h.ts.GetAgentTask(r.Context(), taskID)
	if err != nil {
		errInternalServer(w, err.Error())
		return nil, false
	}
	if t == nil || !scope.owns(t.AgentName, t.SessionID) {
		errNotFound(w, "task not found")
		return nil, false
	}
	return t, true
}

// ownNote reports whether the note is in scope, answering 404 when it isn't.
func (h *SessionsHandler) ownNote(w http.ResponseWriter, r *http.Request, scope itemScope, noteID int64) bool {
	n, err := h.ts.GetAgentNote(r.Context(), noteID)
	if err != nil {
		errInternalServer(w, err.Error())
		return false
	}
	if n == nil || !scope.owns(n.AgentName, n.SessionID) {
		errNotFound(w, "note not found")
		return false
	}
	return true
}

func (h *SessionsHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	taskID, _ := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	scope, ok := h.itemScope(w, r, queryClaim(r))
	if !ok {
		return
	}
	if _, ok := h.ownTask(w, r, scope, taskID); !ok {
		return
	}
	var body struct {
		Title     *string `json:"title"`
		Completed *int    `json:"completed"`
		SortOrder *int    `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if err := h.ts.UpdateAgentTask(r.Context(), taskID, body.Title, body.Completed, body.SortOrder); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	go h.notifyPersonalTaskReadiness(context.Background(), taskID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SessionsHandler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	taskID, _ := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	scope, ok := h.itemScope(w, r, queryClaim(r))
	if !ok {
		return
	}
	if _, ok := h.ownTask(w, r, scope, taskID); !ok {
		return
	}
	if err := h.ts.DeleteAgentTask(r.Context(), taskID); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SessionsHandler) ReorderTasks(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.itemScope(w, r, queryClaim(r))
	if !ok {
		return
	}
	var body struct {
		TaskIDs []int64 `json:"task_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.TaskIDs) == 0 {
		errBadRequest(w, "task_ids required")
		return
	}
	for _, id := range body.TaskIDs {
		if _, ok := h.ownTask(w, r, scope, id); !ok {
			return // nothing reordered: every id must be in scope
		}
	}
	if err := h.ts.ReorderAgentTasks(r.Context(), scope.name, body.TaskIDs); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Notes ───────────────────────────────────────────────────────────────

func (h *SessionsHandler) ListNotes(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)
	notes, err := h.ts.ListAgentNotes(r.Context(), name, sidPtr)
	if err != nil || notes == nil {
		notes = []store.AgentNote{}
	}
	writeJSON(w, http.StatusOK, notes)
}

func (h *SessionsHandler) CreateNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content   string `json:"content"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		errBadRequest(w, "content is required")
		return
	}
	claim := body.SessionID
	if claim == "" {
		claim = queryClaim(r)
	}
	scope, ok := h.itemScope(w, r, claim) // whose notes (#3498)
	if !ok {
		return
	}
	name := scope.name
	if scope.sid != nil {
		body.SessionID = *scope.sid
	}
	note, err := h.ts.CreateAgentNote(r.Context(), name, body.Content, strPtr(body.SessionID))
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, note)
}

func (h *SessionsHandler) UpdateNote(w http.ResponseWriter, r *http.Request) {
	noteID, _ := strconv.ParseInt(chi.URLParam(r, "noteID"), 10, 64)
	scope, ok := h.itemScope(w, r, queryClaim(r))
	if !ok {
		return
	}
	if !h.ownNote(w, r, scope, noteID) {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		errBadRequest(w, "content is required")
		return
	}
	if err := h.ts.UpdateAgentNote(r.Context(), noteID, body.Content); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SessionsHandler) DeleteNote(w http.ResponseWriter, r *http.Request) {
	noteID, _ := strconv.ParseInt(chi.URLParam(r, "noteID"), 10, 64)
	scope, ok := h.itemScope(w, r, queryClaim(r))
	if !ok {
		return
	}
	if !h.ownNote(w, r, scope, noteID) {
		return
	}
	if err := h.ts.DeleteAgentNote(r.Context(), noteID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Events ──────────────────────────────────────────────────────────────

func (h *SessionsHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)
	limit := clampLimit(queryInt(r, "limit", 50), 50, 200)
	events, err := h.ts.ListAgentEvents(r.Context(), name, limit, sidPtr)
	if err != nil || events == nil {
		events = []store.AgentEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *SessionsHandler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		EventType  string `json:"event_type"`
		Summary    string `json:"summary"`
		ToolName   string `json:"tool_name"`
		SessionID  string `json:"session_id"`
		DetailJSON any    `json:"detail_json"`
		DurationMs *int64 `json:"duration_ms"`
		ToolUseID  string `json:"tool_use_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.EventType == "" || body.Summary == "" {
		errBadRequest(w, "event_type and summary required")
		return
	}
	// Who may post (#3479, Security): these events decide an agent's state, and from it
	// when Coral types into the agent (#3475), so an event is accepted only from that
	// agent's own hooks, identified by its session token, in every mode. The token alone
	// decides the session: a session_id naming another one is refused, a missing one is
	// filled in. The path's name is not checked; it comes from the hook's working folder
	// and can name another agent in the same folder, so the session's own name is used.
	// The one event the dashboard posts, a goal edited by the Operator, is also accepted
	// from the Operator. Coral Hub can't reach this route (TierNever).
	owner, hasToken := VerifiedSession(r.Context())
	switch {
	case hasToken:
		if body.SessionID != "" && body.SessionID != owner.SessionID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "identity_mismatch",
				"message": "An agent can post events only for its own session"})
			return
		}
		body.SessionID = owner.SessionID
	case body.EventType == "goal" && humanInputCaller(r):
	default:
		// Logged once per claimed session (an agent's hooks post several events a
		// minute), so an agent that predates session tokens is easy to spot.
		if claimed := body.SessionID + "|" + name; refusedEventSenders.add(claimed) {
			slog.Warn("agent events refused: no session token (restart the agent from Coral to restore its status)",
				"session", name, "session_id", body.SessionID, "event_type", body.EventType, "tool", r.UserAgent())
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session_token_required",
			"message": "Agent events come from the agent's own hooks, with its session token. Restart the agent from Coral if it predates session tokens."})
		return
	}
	if !postableEventTypes[body.EventType] {
		errBadRequest(w, "unknown event_type "+strconv.Quote(body.EventType))
		return
	}
	if len(body.Summary) > maxEventSummaryBytes {
		body.Summary = truncateUTF8(body.Summary, maxEventSummaryBytes)
	}
	if body.EventType == "tool_use" && body.DurationMs == nil && body.SessionID != "" {
		if ms, ok := h.pending.durationMs(body.SessionID, body.ToolUseID, time.Now()); ok {
			body.DurationMs = &ms
		}
	}

	// If session_id is provided, look up the actual agent name from the live
	// sessions DB. This is more reliable than the URL path name, which comes
	// from the hook's cwd and can be wrong when multiple agents share a directory.
	agentName := name
	if body.SessionID != "" {
		if ls, err := h.ss.GetLiveSession(r.Context(), body.SessionID); err == nil && ls != nil {
			agentName = ls.AgentName
		}
	}

	event := &store.AgentEvent{
		AgentName: agentName,
		EventType: body.EventType,
		Summary:   body.Summary,
	}
	if body.SessionID != "" {
		event.SessionID = &body.SessionID
	}
	if body.ToolName != "" {
		event.ToolName = &body.ToolName
	}
	if body.DurationMs != nil && *body.DurationMs >= 0 {
		event.DurationMs = body.DurationMs
	}
	if body.ToolUseID != "" {
		event.RefID = &body.ToolUseID
	}
	// A goal posted here without a source is the operator's edit; the goal
	// generator never replaces it.
	if body.EventType == "goal" && body.DetailJSON == nil {
		body.DetailJSON = map[string]string{"source": "user"}
	}
	// A request's kind is shown on the dashboard: only the known kinds are stored (#3481,
	// QA's review). The event comes from the agent's own hook, but its text is the agent's.
	if body.EventType == "permission_request" || body.EventType == "request_resolved" {
		if d, ok := body.DetailJSON.(map[string]any); ok {
			if k, _ := d["kind"].(string); !sessionstate.RequestKinds[k] {
				d["kind"] = ""
			}
		}
	}
	if body.DetailJSON != nil {
		djBytes, err := json.Marshal(body.DetailJSON)
		if err != nil {
			slog.Warn("failed to marshal detail_json", "error", err)
		} else if len(djBytes) > maxEventDetailBytes {
			errBadRequest(w, "detail_json is too large")
			return
		} else {
			s := string(djBytes)
			event.DetailJSON = &s
		}
	}

	created, err := h.ts.InsertAgentEvent(r.Context(), event)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	h.notePendingToolEvent(body.SessionID, body.EventType, body.ToolUseID)
	if hasToken && body.EventType == "session_end" {
		// The agent's process ended (Claude's SessionEnd): nothing held for it may be
		// typed into whatever runs in that terminal next (#3481). Not on SessionStart:
		// a launch's first prompt is a held send waiting for that very start.
		ptymanager.DropHeld(owner.SessionName, "the agent's session ended")
	}
	// Only the agent's own (token-verified) notification wakes auto-accept early; the sweep
	// itself acts only on the state derived from such events.
	if hasToken && sessionstate.Classify(body.Summary) == sessionstate.NotificationNeedsInput {
		h.autoAccept.Kick() // an auto-accept session's permission prompt is answered now, not on the next sweep
	}
	writeJSON(w, http.StatusOK, created)
}

// refusedEventSenders remembers which claimed sessions were already logged as refused
// (bounded: a flood of made-up names can't grow it without limit).
var refusedEventSenders = &onceSet{max: 1000}

type onceSet struct {
	mu   sync.Mutex
	seen map[string]bool
	max  int
}

// add reports whether key is new. Once max keys are remembered it reports false for
// every other key too, so the log stays quiet.
func (s *onceSet) add(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[key] || len(s.seen) >= s.max {
		return false
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	s.seen[key] = true
	return true
}

// postableEventTypes are the event types POST /api/sessions/live/{name}/events accepts
// (#3479): the ones Coral's hooks send, and the dashboard's goal edit. Others are refused,
// never mapped. Coral records its own events (auto_accept, retry, ...) directly.
var postableEventTypes = map[string]bool{
	"session_reset": true, "prompt_submit": true, "tool_use": true, "stop": true, "notification": true, "goal": true,
	// #3481: a request to the user opened (PermissionRequest, Elicitation) or answered
	// (ElicitationResult), and the agent's session ended.
	"permission_request": true, "request_resolved": true, "session_end": true,
}

// Limits on a posted event: a hook's summary is short and its detail a small JSON object
// (tool input previews, truncated responses).
const (
	maxEventSummaryBytes = 2000
	maxEventDetailBytes  = 64 << 10
)

func (h *SessionsHandler) EventCounts(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)
	counts, err := h.ts.GetAgentEventCounts(r.Context(), name, sidPtr)
	if err != nil || counts == nil {
		counts = []store.ToolCount{}
	}
	writeJSON(w, http.StatusOK, counts)
}

func (h *SessionsHandler) ClearEvents(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)
	claimed := ""
	if sidPtr != nil {
		claimed = *sidPtr
	}
	// Only an agent's own token (for its own session) or the Operator clears an
	// agent's events (#3495).
	sid, ok := callerSession(w, r, claimed, true, nil)
	if !ok {
		return
	}
	if owner, isAgent := VerifiedSession(r.Context()); isAgent {
		sidPtr = &sid
		name = owner.SessionName
		if ls, err := h.ss.GetLiveSession(r.Context(), sid); err == nil && ls != nil {
			name = ls.AgentName
		}
	}
	if err := h.ts.ClearAgentEvents(r.Context(), name, sidPtr); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RecordTokenUsage records a token usage snapshot for a session.
// POST /api/sessions/live/{name}/token-usage
func (h *SessionsHandler) RecordTokenUsage(w http.ResponseWriter, r *http.Request) {
	if h.tokenStore == nil {
		errInternalServer(w, "token tracking not available")
		return
	}

	name := chi.URLParam(r, "name")
	var body struct {
		SessionID        string  `json:"session_id"`
		InputTokens      float64 `json:"input_tokens"`
		OutputTokens     float64 `json:"output_tokens"`
		CacheReadTokens  float64 `json:"cache_read_tokens"`
		CacheWriteTokens float64 `json:"cache_write_tokens"`
		CostUSD          float64 `json:"cost_usd"`
		NumTurns         float64 `json:"num_turns"`
		// Model is optional. When the caller does not know it the row stores
		// NULL; the session's configured model is deliberately not substituted,
		// since it may differ from the model that produced these tokens.
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// Only the agent's own hook reports its usage (#3495): the token decides the
	// session, and its agent name is the session's, not the path's.
	sid, ok := callerSession(w, r, body.SessionID, false, nil)
	if !ok {
		return
	}
	body.SessionID = sid

	ctx := r.Context()

	// Resolve team_id and board_name from the live session
	var teamID *int64
	var boardName *string
	var agentType string
	if body.SessionID != "" {
		if ls, err := h.ss.GetLiveSession(ctx, body.SessionID); err == nil && ls != nil {
			if ls.TeamID != nil {
				teamID = ls.TeamID
			}
			if ls.BoardName != nil {
				boardName = ls.BoardName
			}
			agentType = ls.AgentType
			name = ls.AgentName
		}
	}
	if agentType == "" {
		agentType = "claude"
	}

	inputTokens := int(body.InputTokens)
	outputTokens := int(body.OutputTokens)
	cacheReadTokens := int(body.CacheReadTokens)
	cacheWriteTokens := int(body.CacheWriteTokens)

	err := h.tokenStore.RecordUsage(ctx, &store.TokenUsage{
		SessionID:        body.SessionID,
		AgentName:        name,
		AgentType:        agentType,
		TeamID:           teamID,
		BoardName:        boardName,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		TotalTokens:      inputTokens + outputTokens + cacheReadTokens + cacheWriteTokens,
		CostUSD:          body.CostUSD,
		NumTurns:         int(body.NumTurns),
		Model:            body.Model,
	})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// WebSocket handlers are in websocket.go

// ── Helpers ─────────────────────────────────────────────────────────────

func (h *SessionsHandler) findLogPath(agentType, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	if agentType == "" {
		agentType = at.Claude
	}
	return naming.LogFile(h.cfg.LogDir, agentType, sessionID)
}

// writeJSON is a package-local alias for httputil.WriteJSON.
// Kept as a short name since it's called 220+ times across route handlers.
var writeJSON = httputil.WriteJSON

func generateUUID() string {
	b := make([]byte, 16)
	crand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// launchSession creates a new agent session using the specified backend (tmux or pty).
func (h *SessionsHandler) launchSession(ctx context.Context, workDir, agentType, displayName, resumeSessionID string,
	flags []string, prompt, boardName, boardServer, backend, boardType, model string, capabilities *agent.Capabilities,
	tools []string, hooks map[string]interface{}) (_ map[string]any, retErr error) {

	if msg := launchDirError(workDir); msg != "" {
		return nil, errors.New(msg)
	}
	absDir, _ := filepath.Abs(workDir)

	if agentType == "" {
		agentType = at.Claude
	}
	// A role another running agent already holds on this board gets a numbered name
	// ("Backend Dev 2"), so the two never share a subscription, mentions or read state
	// (#3543). On this Coral's boards only; a remote board's Coral decides its own.
	if boardName != "" && (boardServer == "" || boardServer == "local") {
		displayName = h.uniqueBoardRole(ctx, boardName, displayName, agentType)
		picked := naming.SubscriberID(displayName, agentType)
		defer func() {
			if retErr != nil {
				h.releaseRoleReservation(boardName, picked) // the launch failed: the name is free again
			}
		}()
	}
	if err := validateAgentModel(agentType, model); err != nil {
		return nil, err
	}

	// Resolve custom CLI path from settings
	userSettings, _ := h.ss.GetSettings(ctx)
	cliPath := agent.ResolveCLIPath(userSettings, agentType)

	// Check CLI availability before launching (skip for terminal type)
	if agentType != at.Terminal {
		checkBin := cliPath
		if checkBin == "" {
			if info := agent.GetCLIInfo(agentType); info != nil {
				checkBin = info.Binary
			}
		}
		if checkBin != "" {
			if resolved, err := exec.LookPath(checkBin); err != nil {
				// LookPath failed — try common install locations
				if found := agent.FindCLIInCommonPaths(checkBin); found != "" {
					cliPath = found
					log.Printf("[launch] %s not on PATH, found at %s", checkBin, found)
				} else {
					info := agent.GetCLIInfo(agentType)
					installCmd := ""
					if info != nil {
						installCmd = info.InstallCommand
					}
					return nil, fmt.Errorf("%s CLI not found. Install it: %s", checkBin, installCmd)
				}
			} else if cliPath == "" {
				cliPath = resolved
			}
		}
	}

	if err := agent.ValidateAgentType(agentType); err != nil {
		return nil, err
	}

	// NOTE: this value selects the LAUNCH PATH, not the terminal a session ends
	// up on. "pty" means "go through the TerminalBackend abstraction", which is
	// how tmux-backed sessions are launched too — h.backend is a TmuxBackend in
	// that case. It is persisted and drives the same branch on wake, so it must
	// keep meaning exactly this. Use terminalKind below for what actually runs.
	// When the server is using a PTY backend, always use PTY regardless of
	// what the frontend requests — tmux is not available on this platform.
	if _, isPTY := h.backend.(*ptymanager.PTYBackend); isPTY {
		backend = "pty"
	} else if backend == "" {
		if h.backend != nil {
			backend = "pty"
		} else {
			backend = "tmux"
		}
	}
	folderName := filepath.Base(absDir)

	sessionID := generateUUID()
	sessionName := naming.SessionName(agentType, sessionID)
	logFile := naming.LogFile(h.cfg.LogDir, agentType, sessionID)

	isTerminal := agentType == at.Terminal
	agentImpl := agent.GetAgent(agentType)
	if resumeSessionID != "" && !isTerminal {
		agent.TryPrepareResume(agentImpl, resumeSessionID, absDir)
	}

	role := naming.SubscriberID(displayName, agentType)

	launchParams := agent.LaunchParams{
		SessionID:       sessionID,
		SessionName:     sessionName,
		ProtocolPath:    h.protocolPath(),
		ResumeSessionID: resumeSessionID,
		Flags:           flags,
		WorkingDir:      absDir,
		BoardName:       boardName,
		BoardLabel:      h.boardLabel(boardName),
		Role:            role,
		Prompt:          prompt,
		PromptOverrides: promptOverrides(userSettings),
		BoardType:       boardType,
		Capabilities:    capabilities,
		Tools:           tools,
		Hooks:           hooks,
		CLIPath:         cliPath,
		PermissionMode:  userSettings["default_permission_mode"],
		CoralDir:        h.cfg.CoralDir(),
		CoralHost:       h.cfg.AgentHost(),
		CoralPort:       h.cfg.Port,
	}
	if cliPath != "" {
		log.Printf("[launch] using custom CLI path: %s", cliPath)
	}

	// The launch command is built before any terminal is made: when its prompt or settings
	// files can't be written, the launch fails with nothing left behind (#3601 W12).
	var cmd string
	if !isTerminal {
		var err error
		if cmd, err = launchCommand(agentImpl, launchParams); err != nil {
			return nil, err
		}
	}

	if backend == "pty" && h.backend != nil {
		// PTY backend: spawn the agent process directly
		// Spawn a shell first (empty command), then send the agent command as input.
		// This matches the tmux pattern and works cross-platform — the shell
		// interprets bash syntax like $(cat ...) correctly.
		if err := h.backend.Spawn(sessionName, agentType, absDir, sessionID, "", 200, 50); err != nil {
			return nil, fmt.Errorf("pty spawn failed: %w", err)
		}
		// PTY backend manages its own log file
		logFile = h.backend.LogPath(sessionName)

		// Wait for shell to initialize, then send the launch command
		if !isTerminal && cmd != "" {
			log.Printf("[launch] pty session=%s agent=%s cmd=%s", sessionName, agentType, cmd)
			if pb, ok := h.backend.(*ptymanager.PTYBackend); ok {
				if !pb.WaitReady(sessionName, 5*time.Second) {
					log.Printf("[launch] pty shell not ready after 5s, sending anyway: %s", sessionName)
				}
			} else {
				time.Sleep(500 * time.Millisecond)
			}
			if err := sendLaunchCommand(ctx, h.terminal, sessionName, cmd); err != nil {
				return nil, fmt.Errorf("pty launch command failed: %w", err)
			}
		}
	} else {
		// Tmux backend: create session, pipe-pane, send keys
		backend = "tmux" // normalize if pty requested but no backend available

		// Only check real tmux availability when the terminal is the real
		// tmux terminal — tests inject a mock that should bypass this.
		if tmuxTerm, isTmux := h.terminal.(*ptymanager.TmuxSessionTerminal); isTmux {
			tmuxPath, ok := tmux.IsAvailable()
			slog.Info("launch tmux availability",
				"available", ok,
				"resolved_path", tmuxPath,
				"client_tmux_bin_before", tmuxTerm.Client().TmuxBin,
				"socket", tmuxTerm.Client().SocketPath,
				"path", os.Getenv("PATH"),
			)
			if !ok {
				return nil, fmt.Errorf("tmux is required to launch agents but was not found on this system. Install tmux (e.g. 'brew install tmux' on macOS), then try again")
			}
			if tmuxPath != "" && tmuxTerm.Client().TmuxBin != tmuxPath {
				slog.Info("launch updating tmux client binary",
					"from", tmuxTerm.Client().TmuxBin,
					"to", tmuxPath,
				)
				tmuxTerm.Client().TmuxBin = tmuxPath
			}
		}

		// Create empty log file
		ptymanager.ResetAgentLog(logFile) // 0600, never through a link (#3601, W17)

		slog.Info("launch creating tmux session",
			"session_name", sessionName,
			"working_dir", absDir,
			"terminal_type", fmt.Sprintf("%T", h.terminal),
		)
		if err := h.terminal.CreateSession(ctx, sessionName, absDir); err != nil {
			slog.Error("launch tmux CreateSession failed",
				"session_name", sessionName,
				"working_dir", absDir,
				"terminal_type", fmt.Sprintf("%T", h.terminal),
				"error", err,
			)
			return nil, fmt.Errorf("tmux new-session failed: %w", err)
		}
		// Put the Coral environment into the tmux session so that anything the
		// user runs by hand in that pane — coral-board especially — reaches the
		// server that launched the agent. Same source as the agent's own env.
		if tmuxTerm, ok := h.terminal.(*ptymanager.TmuxSessionTerminal); ok {
			for _, kv := range agent.CoralEnv(launchParams) {
				if err := tmuxTerm.Client().SetEnvironment(ctx, sessionName, kv[0], kv[1]); err != nil {
					log.Printf("[launch] failed to set %s for %s: %v", kv[0], sessionName, err)
				}
			}
			// Prepend Coral tools dir to PATH so coral-board and hooks are
			// discoverable by subshells Claude spawns (e.g. Bash tool).
			if binDir := agent.CoralToolsDir(); binDir != "" {
				currentPath := os.Getenv("PATH")
				if !strings.Contains(currentPath, binDir) {
					currentPath = binDir + ":" + currentPath
				}
				if err := tmuxTerm.Client().SetEnvironment(ctx, sessionName, "PATH", currentPath); err != nil {
					log.Printf("[launch] failed to set PATH for %s: %v", sessionName, err)
				}
			}
		}

		// Setup pipe-pane logging
		h.terminal.StartLogging(ctx, sessionName, logFile)

		// Set pane title using native tmux command (avoids shell echo)
		h.terminal.SetPaneTitle(ctx, sessionName+".0", fmt.Sprintf("%s — %s", folderName, agentType))

		// Launch the agent (unless terminal)
		if !isTerminal {
			log.Printf("[launch] tmux session=%s agent=%s cmd=%s", sessionName, agentType, cmd)
			if err := sendLaunchCommand(ctx, h.terminal, sessionName+".0", cmd); err != nil {
				return nil, err
			}
		}
	}

	// Capture shell PID for process-tree-based identity resolution.
	// Must happen after CreateSession (tmux session exists) but the pane PID
	// is available immediately — it's the shell spawned by tmux new-session.
	shellPID, pidErr := launchedSessionPID(ctx, backend, h.backend, h.terminal, sessionName)
	if pidErr != nil {
		log.Printf("[launch] failed to capture PID for session %s: %v", sessionName, pidErr)
	} else if shellPID > 0 {
		log.Printf("[launch] captured PID %d for session %s", shellPID, sessionName)
	}

	// Register in DB
	h.chatMeta.forget(sessionID, sessionName, folderName)
	h.ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:     sessionID,
		AgentType:     agentType,
		AgentName:     folderName,
		WorkingDir:    absDir,
		DisplayName:   strPtr(displayName),
		ResumeFromID:  strPtr(resumeSessionID),
		Flags:         store.MarshalFlags(flags),
		Prompt:        strPtr(prompt),
		BoardName:     strPtr(boardName),
		BoardServer:   strPtr(boardServer),
		Backend:       strPtr(backend),
		BoardType:     strPtr(boardType),
		Capabilities:  store.MarshalCapabilities(capabilities),
		Model:         strPtr(model),
		ContextWindow: proxy.LookupContextWindow(model),
		Tools:         store.MarshalCapabilities(tools),
		PID:           shellPID,
	})
	if agentType != at.Terminal {
		go h.recordCLIVersion(sessionID, agentType, userSettings) // #3481
	}

	if displayName != "" {
		h.ss.SetDisplayName(ctx, sessionID, displayName)
	}

	return map[string]any{
		"ok": true, "session_id": sessionID, "session_name": sessionName,
		"agent_type": agentType, "working_dir": absDir,
		"display_name": displayName, // numbered when the role was taken on the board (#3543)
		"log_file":     logFile, "backend": backend,
		// backend names the launch path and reads as "pty" even for a tmux
		// session; terminal names what the session actually runs on. Reporting
		// only the former is how a silent backend downgrade went unnoticed.
		"terminal": terminalKind(h.backend),
	}, nil
}

// validateAgentModel rejects only known Coral model IDs whose provider is
// incompatible with the selected agent implementation. Unknown IDs remain
// valid for custom/provider-specific configurations.
func validateAgentModel(agentType, model string) error {
	provider, known := proxy.KnownModelProvider(model)
	if !known {
		return nil
	}
	var expected string
	switch agentType {
	case at.Claude:
		expected = "anthropic"
	case at.Codex:
		expected = "openai"
	case at.Agy, at.Antigravity, at.Gemini:
		expected = "google"
	default:
		return nil
	}
	if provider != expected {
		return fmt.Errorf("model %q belongs to %s provider and cannot launch with %s agent; choose a compatible model or leave custom IDs unchanged", model, provider, agentType)
	}
	return nil
}

// launchedSessionPID resolves the process identifier from what actually owns
// the launched session. The persisted backend label describes the launch path
// and can be "pty" even when h.backend is a TmuxBackend, so it must not be the
// sole condition for PID capture.
func launchedSessionPID(ctx context.Context, backend string, terminalBackend ptymanager.TerminalBackend, terminal ptymanager.SessionTerminal, sessionName string) (int, error) {
	if provider, ok := terminalBackend.(ptymanager.SessionPIDProvider); ok {
		return provider.SessionPID(ctx, sessionName)
	}
	if backend == "tmux" {
		if tmuxTerm, ok := terminal.(*ptymanager.TmuxSessionTerminal); ok {
			return tmuxTerm.Client().GetPanePID(ctx, sessionName)
		}
	}
	return 0, nil
}

// terminalKind names the terminal a session actually runs on, as opposed to
// the launch path taken to get there.
func terminalKind(b ptymanager.TerminalBackend) string {
	switch b.(type) {
	case *ptymanager.PTYBackend:
		return "pty"
	case *ptymanager.TmuxBackend:
		return "tmux"
	case nil:
		return "tmux"
	}
	return "unknown"
}

// setupBoardAndPrompt subscribes a session to a message board.
// The agent prompt is now passed directly as a CLI positional argument
// in launchSession, so no tmux-based prompt delivery is needed.
// subscriberID is the stable identity (role name), sessionName is the mutable tmux session.
func (h *SessionsHandler) setupBoardAndPrompt(sessionID, sessionName, agentType, boardName, displayName string) {
	role := naming.SubscriberID(displayName, agentType)
	subscriberID := role // stable board identity = role name
	ctx := context.Background()

	if boardName == "" {
		return
	}

	// Board subscription (immediate — no delay needed)
	if h.bs != nil {
		isOrchestrator := strings.Contains(strings.ToLower(role), "orchestrator")
		receiveMode := "mentions"
		if isOrchestrator {
			receiveMode = "all"
		}
		if agentType == "terminal" {
			receiveMode = "none"
		}

		// Preserve existing receive_mode on re-subscribe (e.g. restart).
		// Use session-name lookup to find the subscription for THIS session,
		// not a stale one from a previous board with the same subscriber_id.
		existing, err := h.bs.GetSubscriptionBySessionName(ctx, sessionName)
		if err == nil && existing != nil && existing.ReceiveMode != "" {
			receiveMode = existing.ReceiveMode
		}

		if _, err := h.bs.Subscribe(ctx, boardName, subscriberID, role, sessionName, nil, receiveMode, isOrchestrator); err != nil {
			log.Printf("Failed to subscribe session %s to board %s: %v", sessionID[:8], boardName, err)
		}
		// The subscription now holds the name; the reservation that covered the launch goes.
		h.releaseRoleReservation(boardName, subscriberID)
	}

	// Write local board state file so coral-board CLI can find its subscription
	writeBoardStateFile(sessionName, boardName, role, h.cfg)
}

// writeBoardStateFile writes the local board state file that coral-board CLI
// reads to determine which board a session is subscribed to.
func writeBoardStateFile(sessionName, boardName, role string, cfg *config.Config) {
	if !plainFileName(sessionName) {
		return // only a plain name can be part of a file name (#3602 W25)
	}
	stateDir := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		stateDir = filepath.Join(home, ".coral")
	}
	if cfg != nil {
		stateDir = cfg.CoralDir()
	}
	if stateDir == "" {
		// No data dir (a Config not built by config.Load, or no home
		// directory): never resolve the file against the working directory.
		return
	}
	testguard.CheckDataPath(stateDir)
	os.MkdirAll(stateDir, 0o700)

	state := map[string]string{
		"project":   boardName,
		"job_title": role,
	}
	// Persist the explicit loopback URL even on the default port. Codex's
	// sandbox can strip CORAL_URL/CORAL_PORT from child processes; without
	// this persisted value coral-board falls back to localhost, which may
	// resolve to IPv6 ::1 and be denied while IPv4 loopback is available.
	if cfg != nil {
		host := cfg.AgentHost() // the loopback listener whenever there is one (#3594)
		state["server_url"] = fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(cfg.Port)))
	}

	data, _ := json.Marshal(state)
	statePath := filepath.Join(stateDir, fmt.Sprintf("board_state_%s.json", sessionName))
	// 0600: it names the agent's board and server (#3581).
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		log.Printf("Failed to write board state file for %s: %v", sessionName, err)
	} else {
		os.Chmod(statePath, 0o600) // a file an older version wrote 0644
	}
}

// removeBoardStateFile deletes the board state file for a session.
// Called when a session is killed so state files don't accumulate.
func removeBoardStateFile(sessionName string, cfg *config.Config) {
	if cfg == nil || cfg.CoralDir() == "" {
		return // no data dir: nothing was written (see writeBoardStateFile)
	}
	if !plainFileName(sessionName) {
		return // nothing was written for it (writeBoardStateFile)
	}
	statePath := filepath.Join(cfg.CoralDir(), fmt.Sprintf("board_state_%s.json", sessionName))
	os.Remove(statePath) // Ignore error — file may not exist
}

// plainFileName reports whether name can be part of one file name on every platform: not empty,
// not "." or "..", and no '/', '\\' or ':' (the session name in a board state file's name comes
// from the URL on kill; coral-board and the hooks look it up as it is, so it isn't rewritten).
func plainFileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\:")
}

func (h *SessionsHandler) protocolPath() string {
	// The copy next to the executable. CoralRoot only when CORAL_ROOT names it explicitly, or in
	// a dev build: it defaults to the home folder, and ~/PROTOCOL.md, which any agent running as
	// this user can write, would otherwise become every later agent's system prompt (#3601,
	// weak site W16, Backend's third site).
	var candidates []string
	if ex, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(ex), "PROTOCOL.md"))
	}
	if h.cfg.CoralRoot != "" && (os.Getenv("CORAL_ROOT") != "" || config.TierName == "dev") {
		candidates = append(candidates,
			filepath.Join(h.cfg.CoralRoot, "PROTOCOL.md"),
			filepath.Join(h.cfg.CoralRoot, "src", "coral", "PROTOCOL.md"))
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// boardProject returns the board project name for a session, checking
// board subscriptions first, then falling back to live_sessions DB.
// boardSubscriberFlag exposes the authoritative CanPeek role without using
// mutable display names or job-title substring matching. Nil means no board.
func boardSubscriberFlag(sub *board.Subscriber) any {
	if sub == nil {
		return nil
	}
	return sub.CanPeek != 0
}

func boardProject(subs map[string]*board.Subscriber, fallback map[string][2]string, tmuxName, sessionID string) any {
	if sub, ok := subs[tmuxName]; ok {
		return sub.Project
	}
	if fb, ok := fallback[sessionID]; ok && fb[0] != "" {
		return fb[0]
	}
	return nil
}

// boardJobTitle returns the board job title for a session.
func boardJobTitle(subs map[string]*board.Subscriber, fallback map[string][2]string, tmuxName, sessionID string) any {
	if sub, ok := subs[tmuxName]; ok {
		return sub.JobTitle
	}
	if fb, ok := fallback[sessionID]; ok && fb[1] != "" {
		return fb[1]
	}
	return nil
}

// ResolvePath turns a file reference from an agent's chat (repo-relative,
// relative to the agent's folder, or absolute, with an optional :line or
// :line-line suffix) into the git-root-relative path the file endpoints use.
// Only existing files inside the agent's git root resolve.
// GET /api/sessions/live/{name}/resolve-path?filepath=...&session_id=...
func (h *SessionsHandler) ResolvePath(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("filepath"))
	if ref == "" || strings.ContainsAny(ref, "\x00") {
		errBadRequest(w, "filepath is required")
		return
	}
	f, err := h.resolveFileRef(r.Context(), chi.URLParam(r, "name"), r.URL.Query().Get("session_id"), ref, strictBounds(r))
	if err != nil {
		writeResolveErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"filepath": f.rel, "line": f.line})
}

type resolvedFileRef struct {
	rel  string // relative to the git root, slash-separated
	abs  string // absolute, symlinks resolved
	line int
}

var (
	errNoWorkdir    = errors.New("Could not determine working directory")
	errFileNotFound = errors.New("File not found")
)

func writeResolveErr(w http.ResponseWriter, err error) {
	if err == errNoWorkdir {
		errBadRequest(w, err.Error())
	} else {
		errNotFound(w, err.Error())
	}
}

func (h *SessionsHandler) resolveFileRef(ctx context.Context, name, sessionID, ref string, strict bool) (resolvedFileRef, error) {
	fp, line := strings.TrimSpace(ref), 0
	if unescaped, err := url.PathUnescape(fp); err == nil {
		fp = unescaped
	}
	if strings.HasPrefix(strings.ToLower(fp), "file://localhost/") {
		fp = fp[len("file://localhost"):]
	}
	fp = strings.TrimPrefix(fp, "file://")

	if m := fileRefHashLineRe.FindStringSubmatch(fp); m != nil {
		fp = m[1]
		line, _ = strconv.Atoi(m[2])
	} else if m := fileRefLineRe.FindStringSubmatch(fp); m != nil {
		fp = m[1]
		line, _ = strconv.Atoi(m[2])
	} else if idx := strings.Index(fp, "#"); idx != -1 {
		fp = fp[:idx]
	}

	// ~/path is the host user's home directory
	if fp == "~" || strings.HasPrefix(fp, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			fp = filepath.Join(home, strings.TrimPrefix(fp, "~"))
		}
	}

	root := h.resolveGitRoot(ctx, name, "", sessionID)
	if root == "" {
		root = h.resolveWorkdir(ctx, name, "", sessionID)
	}
	if root == "" {
		return resolvedFileRef{}, errNoWorkdir
	}
	realRoot, err := safefs.Resolve(root)
	if err != nil {
		return resolvedFileRef{}, errNoWorkdir
	}
	if strict && !rootAllowed(realRoot) {
		return resolvedFileRef{}, errFileNotFound // same answer as any missing file
	}

	check := func(c string) (resolvedFileRef, bool) {
		real, err := safefs.Resolve(c)
		if err != nil {
			return resolvedFileRef{}, false
		}
		if info, err := os.Stat(real); err != nil || info.IsDir() {
			return resolvedFileRef{}, false
		}
		if !strings.HasPrefix(real, realRoot+string(os.PathSeparator)) {
			// Local dashboard: absolute paths open read-only wherever they are. Through
			// the tunnel nothing outside the repository resolves, and the answer is the
			// same as for a missing file (no existence oracle).
			if filepath.IsAbs(fp) && !strict {
				return resolvedFileRef{rel: real, abs: real, line: line}, true
			}
			return resolvedFileRef{}, false
		}
		if sensitivePath(real) {
			return resolvedFileRef{}, false
		}
		rel, err := filepath.Rel(realRoot, real)
		if err != nil {
			return resolvedFileRef{}, false
		}
		return resolvedFileRef{rel: filepath.ToSlash(rel), abs: real, line: line}, true
	}

	wd := h.resolveWorkdir(ctx, name, "", sessionID)
	var candidates []string
	if filepath.IsAbs(fp) {
		candidates = []string{fp}
	} else {
		candidates = []string{filepath.Join(root, fp)}
		if wd != "" && wd != root {
			candidates = append(candidates, filepath.Join(wd, fp))
		}
	}
	for _, c := range candidates {
		if f, ok := check(c); ok {
			return f, nil
		}
	}

	// Agents often shorten paths (static/app.js for a file several folders
	// down), so fall back to repo files whose path ends with the reference.
	for _, rel := range suffixMatches(ctx, root, wd, fp, h.changedFileSet(ctx, name, sessionID)) {
		if f, ok := check(filepath.Join(root, rel)); ok {
			return f, nil
		}
	}
	return resolvedFileRef{}, errFileNotFound
}

// changedFileSet is the agent's changed files (git-root relative) from the
// last Files refresh.
func (h *SessionsHandler) changedFileSet(ctx context.Context, name, sessionID string) map[string]bool {
	var sid *string
	if sessionID != "" {
		sid = &sessionID
	}
	diffMode := h.getDiffMode(ctx)
	if diffMode == "" {
		diffMode = "branch_point"
	}
	files, _, _ := h.gs.GetChangedFiles(ctx, name, sid, diffMode)
	set := make(map[string]bool, len(files))
	for _, f := range files {
		set[f.Filepath] = true
	}
	return set
}

// suffixMatches lists the repo files (tracked, or untracked and not ignored)
// whose path ends with ref on a folder boundary, best guess first: files the
// agent has changed, then files under its working directory, then the
// shallowest.
func suffixMatches(ctx context.Context, root, workdir, ref string, changed map[string]bool) []string {
	ref = strings.TrimPrefix(filepath.ToSlash(ref), "./")
	if ref == "" || strings.HasPrefix(ref, "../") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "~") ||
		strings.ContainsAny(ref, "*?[]\\:") {
		return nil
	}
	out, err := gitCmd(ctx, root, "ls-files", "-z", "-c", "-o", "--exclude-standard", "--", "*/"+ref).Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var matches []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && strings.HasSuffix(p, "/"+ref) && !seen[p] {
			seen[p] = true
			matches = append(matches, p)
		}
	}

	wdPrefix := ""
	realRoot, err1 := safefs.Resolve(root)
	realWd, err2 := safefs.Resolve(workdir)
	if rel, err := filepath.Rel(realRoot, realWd); err1 == nil && err2 == nil && err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		wdPrefix = filepath.ToSlash(rel) + "/"
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if ci, cj := changed[matches[i]], changed[matches[j]]; ci != cj {
			return ci
		}
		ii, ij := wdPrefix != "" && strings.HasPrefix(matches[i], wdPrefix), wdPrefix != "" && strings.HasPrefix(matches[j], wdPrefix)
		if ii != ij {
			return ii
		}
		di, dj := strings.Count(matches[i], "/"), strings.Count(matches[j], "/")
		if di != dj {
			return di < dj
		}
		return matches[i] < matches[j]
	})
	return matches
}

// A trailing :line, :line:col, :line-line, or #Lline hash on a file reference.
var (
	fileRefLineRe     = regexp.MustCompile(`^(.+?):(\d+)(?:[-:]\d+)?$`)
	fileRefHashLineRe = regexp.MustCompile(`^(.+?)#(?:L)?(\d+)(?:[-:]L?\d+)?$`)
)

// GetFileContent returns the raw content of a file in the agent's working tree.
// GET /api/sessions/live/{name}/file-content?filepath=...&session_id=...
func (h *SessionsHandler) GetFileContent(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	fp := r.URL.Query().Get("filepath")
	sessionID := r.URL.Query().Get("session_id")

	if fp == "" || strings.HasPrefix(fp, "-") || strings.ContainsAny(fp, "\x00") {
		errBadRequest(w, "filepath is required")
		return
	}

	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		errBadRequest(w, "Could not determine working directory")
		return
	}

	strict := strictBounds(r)
	fullPath, err := filepath.Abs(filepath.Join(workdir, fp))
	tempFile := false
	if filepath.IsAbs(fp) {
		if strict {
			// Through the tunnel an absolute path is only honoured inside the session's
			// own repository; anything else looks exactly like a missing file.
			real := realPath(fp)
			if !withinAny(h.sessionFileRoots(r, name, sessionID), real) {
				errNotFound(w, "File not found")
				return
			}
			fullPath = real
		} else if real, rerr := safefs.Resolve(fp); rerr == nil {
			// Local dashboard: absolute paths are read-only, wherever they are.
			fullPath, err, tempFile = real, nil, true
		}
	}
	if err != nil {
		errBadRequest(w, "invalid path")
		return
	}
	if strict && !rootAllowed(realPath(workdir)) {
		errNotFound(w, "File not found")
		return
	}
	if !tempFile && !isPathWithinDir(workdir, fullPath) {
		if strict {
			errNotFound(w, "File not found")
		} else {
			errForbidden(w, "Path traversal not allowed")
		}
		return
	}
	if sensitivePath(realOrSelf(fullPath)) {
		errNotFound(w, "File not found")
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		errNotFound(w, "File not found")
		return
	}
	if info.Size() > maxFileBytes() {
		errPayloadTooLarge(w, "File too large to preview")
		return
	}

	f, err := os.Open(fullPath)
	if err != nil {
		errInternalServer(w, "could not read file")
		return
	}
	content, err := io.ReadAll(io.LimitReader(f, maxFileBytes()+1))
	f.Close()
	if err != nil {
		errInternalServer(w, "could not read file")
		return
	}
	if int64(len(content)) > maxFileBytes() {
		errPayloadTooLarge(w, "File too large to preview")
		return
	}
	if r.URL.Query().Get("raw") == "1" {
		writeRawFilePreview(w, fp, content)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"filepath":          fp,
		"content":           string(content),
		"working_directory": workdir,
	})
}

// Image types the file preview can display, by extension.
var previewImageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".svg":  "image/svg+xml",
}

var previewMediaTypes = map[string]string{
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg",
	".m4a": "audio/mp4", ".aac": "audio/aac", ".flac": "audio/flac",
	".mp4": "video/mp4", ".webm": "video/webm", ".ogv": "video/ogg", ".mov": "video/quicktime", ".m4v": "video/mp4",
}

// writeRawImage serves an image's bytes for the preview's <img> tags (the
// ?raw=1 form of file-content and file-original). Only image types are
// served, and an SVG opened directly cannot run script on this origin.
func writeRawImage(w http.ResponseWriter, fp string, data []byte) {
	ct, ok := previewImageTypes[strings.ToLower(filepath.Ext(fp))]
	if !ok {
		errBadRequest(w, "raw content is only served for images")
		return
	}
	writeRawPreview(w, ct, data, true)
}

func writeRawPreview(w http.ResponseWriter, ct string, data []byte, image bool) {
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	if image {
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	} else {
		// Audio/video are inert media resources; allow the native player to
		// fetch the bytes without applying a document sandbox.
		h.Set("Content-Security-Policy", "default-src 'none'; media-src 'self'")
	}
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func writeRawMedia(w http.ResponseWriter, fp string, data []byte) {
	ct, ok := previewMediaTypes[strings.ToLower(filepath.Ext(fp))]
	if !ok {
		errBadRequest(w, "raw content is only served for previewable media")
		return
	}
	writeRawPreview(w, ct, data, false)
}

func writeRawFilePreview(w http.ResponseWriter, fp string, data []byte) {
	ext := strings.ToLower(filepath.Ext(fp))
	if _, ok := previewImageTypes[ext]; ok {
		writeRawImage(w, fp, data)
		return
	}
	if _, ok := previewMediaTypes[ext]; ok {
		writeRawMedia(w, fp, data)
		return
	}
	errBadRequest(w, "raw content is only served for previewable media")
}

// GetFileOriginal returns the original (git base) content of a file.
// GET /api/sessions/live/{name}/file-original?filepath=...&session_id=...
func (h *SessionsHandler) GetFileOriginal(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	fp := r.URL.Query().Get("filepath")
	sessionID := r.URL.Query().Get("session_id")

	if fp == "" || strings.HasPrefix(fp, "-") || strings.ContainsAny(fp, "\x00:") {
		errBadRequest(w, "filepath is required")
		return
	}

	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		errBadRequest(w, "Could not determine working directory")
		return
	}

	// Path traversal protection
	fullPath, err := filepath.Abs(filepath.Join(workdir, fp))
	if err != nil {
		errBadRequest(w, "invalid path")
		return
	}
	if !isPathWithinDir(workdir, fullPath) {
		errForbidden(w, "path traversal not allowed")
		return
	}
	// The same bounds as Diff for anything but this computer's own dashboard (#3596 W2): the
	// file at the diff base can hold what the working tree no longer shows (a committed .env).
	if strictBounds(r) && diffPathRefused(workdir, fp, fullPath) {
		errNotFound(w, "File not found")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	base := gitutil.GetDiffBase(ctx, workdir, h.getDiffMode(r.Context()))

	// git show <ref>:<path> needs paths relative to the repo root, not the workdir.
	prefix := gitutil.ShowPrefix(ctx, workdir)

	raw := r.URL.Query().Get("raw") == "1"
	out, err := gitutil.Command(ctx, workdir, "show", base+":"+prefix+fp).Output()
	if err != nil {
		if raw {
			errNotFound(w, "File is new since the diff base")
			return
		}
		// File doesn't exist in the base commit (new file)
		writeJSON(w, http.StatusOK, map[string]any{
			"filepath":          fp,
			"content":           "",
			"working_directory": workdir,
		})
		return
	}
	if raw {
		writeRawFilePreview(w, fp, out)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"filepath":          fp,
		"content":           string(out),
		"working_directory": workdir,
	})
}

// SaveFileContent writes content to a file in the agent's working tree.
// PUT /api/sessions/live/{name}/file-content?filepath=...&session_id=...
func (h *SessionsHandler) SaveFileContent(w http.ResponseWriter, r *http.Request) {
	// Limit body size to 50MB
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)

	name := chi.URLParam(r, "name")
	fp := r.URL.Query().Get("filepath")
	sessionID := r.URL.Query().Get("session_id")

	if fp == "" {
		errBadRequest(w, "filepath is required")
		return
	}

	var body struct {
		Content *string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Content == nil {
		errBadRequest(w, "content is required")
		return
	}

	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		errBadRequest(w, "Could not determine working directory")
		return
	}

	// Through safefs (#3596, W1): the working tree is held open as a root, which must still
	// exist (a gone one used to mean "anything goes"); the path is decided on its resolved
	// form, so a symlinked ancestor can't lead out; and the write goes through the root, so
	// nothing swapped in after the check can either.
	root, err := safefs.OpenRoot(workdir)
	if err != nil {
		errBadRequest(w, "the working directory no longer exists")
		return
	}
	defer root.Close()
	target := fp
	if !filepath.IsAbs(target) {
		target = filepath.Join(root.Dir(), target)
	}
	rel, err := safefs.Within(root.Dir(), target)
	if err != nil || rel == "." {
		errForbidden(w, "Path traversal not allowed")
		return
	}

	// Through the tunnel nothing that git runs may be written (hooks, its config, a .git
	// file that points git elsewhere, .husky, a core.hooksPath folder), nor any
	// credential-bearing file. Decided on the resolved target, case-insensitively.
	if strictBounds(r) {
		resolved := filepath.Join(root.Dir(), rel)
		if !rootAllowed(root.Dir()) || gitControlPath(rel) || sensitivePath(resolved) || h.inHooksPath(r.Context(), root.Dir(), rel) {
			errForbidden(w, "Path not allowed")
			return
		}
	}

	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		errInternalServer(w, "could not write file")
		return
	}
	// Decided again on the path the opened file really has, so a folder swapped for a link into
	// .git after the check above can't carry the write there (Security's SHOULD 2).
	var deny func(string) bool
	if strictBounds(r) {
		deny = func(fresh string) bool {
			return gitControlPath(fresh) || sensitivePath(filepath.Join(root.Dir(), fresh)) || h.inHooksPath(r.Context(), root.Dir(), fresh)
		}
	}
	if err := root.WriteFileUnless(rel, []byte(*body.Content), 0o644, deny); err != nil {
		if errors.Is(err, safefs.ErrNotRegular) || errors.Is(err, safefs.ErrLinked) || errors.Is(err, safefs.ErrDenied) || errors.Is(err, safefs.ErrOutside) {
			errForbidden(w, "Path not allowed")
			return
		}
		errInternalServer(w, "could not write file")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "filepath": fp})
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

// ── Team Sleep/Wake ──────────────────────────────────────────────────────

// SleepStatus returns whether a team board is sleeping.
// GET /api/sessions/live/team/{boardName}/sleep-status
func (h *SessionsHandler) SleepStatus(w http.ResponseWriter, r *http.Request) {
	boardName := urlParam(r, "boardName")
	boards, err := h.ss.GetSleepingBoardNames(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"sleeping": false})
		return
	}
	sleeping := false
	for _, b := range boards {
		if b == boardName {
			sleeping = true
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sleeping": sleeping})
}

// KillTeam kills all agents in a team, atomically marking active members as stopped.
// POST /api/sessions/live/team/{boardName}/kill
func (h *SessionsHandler) KillTeam(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	boardName := urlParam(r, "boardName")
	ctx := r.Context()
	bgCtx := context.Background()

	// Get all sessions on this board
	sessions, err := h.ss.GetBoardSessions(ctx, boardName)
	if err != nil {
		errInternalServer(w, "failed to get board sessions: "+err.Error())
		return
	}

	// Atomically stop the team record (snapshots active members with same stopped_at)
	teamWorktree := ""
	if h.teamStore != nil {
		if team, err := h.teamStore.GetTeam(ctx, boardName); err == nil && team != nil {
			h.teamStore.StopTeam(ctx, team.ID)
			if team.IsWorktree == 1 && team.WorkingDir != "" {
				teamWorktree = team.WorkingDir
			}
		}
	}

	// Kill each session
	killed := 0
	for _, ls := range sessions {
		if ls.AgentType != at.Terminal {
			artifactCtx, cancel := context.WithTimeout(bgCtx, 30*time.Second)
			if _, err := h.persistSessionChanges(artifactCtx, ls.SessionID, ls.WorkingDir); err != nil && !errors.Is(err, errNoGitCheckout) {
				slog.Warn("persist team session changes artifact failed", "session_id", ls.SessionID, "error", err)
			}
			cancel()
		}
		h.chatMeta.forget(ls.SessionID)
		h.ss.UnregisterLiveSession(bgCtx, ls.SessionID)
		h.terminal.KillSession(ctx, ls.AgentName, ls.AgentType, ls.SessionID)
		removeBoardStateFile(ls.AgentName, h.cfg)
		agent.CleanupTempFiles(ls.SessionID)
		killed++
	}

	// Artifact snapshots above must finish before an owned worktree is removed.
	if teamWorktree != "" {
		go func() {
			wtCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			parentDir := filepath.Dir(teamWorktree)
			cmd := gitutil.Command(wtCtx, parentDir, "worktree", "remove", "--force", teamWorktree)
			if out, err := cmd.CombinedOutput(); err != nil {
				log.Printf("[kill-team] failed to remove worktree %s: %s", teamWorktree, string(out))
			} else {
				log.Printf("[kill-team] cleaned up worktree: %s", teamWorktree)
			}
		}()
	}

	// Unpause board
	if h.boardHandler != nil {
		h.boardHandler.SetPaused(boardName, false)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions_killed": killed})
}

// ResurrectTeam relaunches a stopped team with only the agents that were active at kill time.
// POST /api/teams/detail/{name}/resurrect
func (h *SessionsHandler) ResurrectTeam(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		errBadRequest(w, "team name required")
		return
	}
	if h.teamStore == nil {
		errInternalServer(w, "team persistence not available")
		return
	}

	ctx := r.Context()
	team, err := h.teamStore.GetTeam(ctx, name)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if team == nil {
		errNotFound(w, "team not found")
		return
	}
	if team.Status != "stopped" {
		errBadRequest(w, "can only resurrect stopped teams (current status: "+team.Status+")")
		return
	}

	// Get members whose stopped_at matches the team's — these were active at kill time
	members, err := h.teamStore.GetMembersForResurrect(ctx, team.ID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if len(members) == 0 {
		errBadRequest(w, "no members eligible for resurrection")
		return
	}

	// Recreate worktree if team had one
	workingDir := team.WorkingDir
	if team.IsWorktree == 1 {
		// Parse base_branch from stored config
		var cfg struct {
			BaseBranch string `json:"base_branch"`
			WorkingDir string `json:"working_dir"`
		}
		json.Unmarshal([]byte(team.ConfigJSON), &cfg)
		branch := cfg.BaseBranch
		if branch == "" {
			branch = "main"
		}
		if err := gitutil.ValidRef(branch); err != nil { // a stored config is checked again (#3601, W24)
			errBadRequest(w, "the team's base_branch: "+err.Error())
			return
		}
		repoDir := cfg.WorkingDir
		if repoDir == "" {
			repoDir = filepath.Dir(team.WorkingDir)
		}

		cmd := gitutil.Command(ctx, repoDir, "worktree", "add", "--", team.WorkingDir, branch)
		if out, err := cmd.CombinedOutput(); err != nil {
			errBadRequest(w, fmt.Sprintf("failed to recreate worktree: %s", string(out)))
			return
		}
		log.Printf("[resurrect] recreated worktree at %s (branch %s)", team.WorkingDir, branch)
	}

	// Relaunch each member
	var launched []map[string]any
	for _, m := range members {
		var agentCfg struct {
			Name         string                 `json:"name"`
			Prompt       string                 `json:"prompt"`
			Capabilities *agent.Capabilities    `json:"capabilities"`
			AgentType    string                 `json:"agent_type"`
			Model        string                 `json:"model"`
			Tools        []string               `json:"tools"`
			Hooks        map[string]interface{} `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(m.AgentConfigJSON), &agentCfg); err != nil {
			log.Printf("[resurrect] failed to parse agent config for %s: %v", m.AgentName, err)
			launched = append(launched, map[string]any{"name": m.AgentName, "error": "invalid agent config"})
			continue
		}

		agentType := agentCfg.AgentType
		if agentType == "" {
			agentType = "claude"
		}

		// Inject distilled knowledge if available
		resurrectPrompt := InjectKnowledgeIntoPrompt(h.cfg.CoralDir(), team.Name, m.AgentName, agentCfg.Prompt)

		result, err := h.launchSession(ctx, workingDir, agentType, m.AgentName,
			"", nil, resurrectPrompt, team.Name, "", "", "", agentCfg.Model, agentCfg.Capabilities,
			agentCfg.Tools, agentCfg.Hooks)
		if err != nil {
			log.Printf("[resurrect] failed to launch %s: %v", m.AgentName, err)
			launched = append(launched, map[string]any{"name": m.AgentName, "error": err.Error()})
			continue
		}

		sessionID := result["session_id"].(string)

		// Update team member: active, new session_id
		h.teamStore.UpdateMemberStatus(ctx, m.ID, "active", &sessionID)
		h.ss.SetTeamID(ctx, sessionID, team.ID)

		// Setup board subscription
		h.setupBoardAndPrompt(sessionID, result["session_name"].(string),
			agentType, team.Name, launchedName(result, m.AgentName))

		launched = append(launched, map[string]any{
			"name": launchedName(result, m.AgentName), "session_id": sessionID, "session_name": result["session_name"],
		})
	}

	// Update team status to running
	h.teamStore.UpdateTeamStatus(ctx, team.ID, "running")

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"team_id": team.ID,
		"board":   team.Name,
		"agents":  launched,
	})
}

// Sleep puts a team to sleep: sets is_sleeping, kills tmux sessions, pauses board.
// POST /api/sessions/live/team/{boardName}/sleep
func (h *SessionsHandler) Sleep(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	boardName := urlParam(r, "boardName")
	ctx := r.Context()

	// Check if any sessions exist on this board
	allLive, _ := h.ss.GetAllLiveSessions(ctx)
	var boardSessions []store.LiveSession
	for _, ls := range allLive {
		if ls.BoardName != nil && *ls.BoardName == boardName {
			boardSessions = append(boardSessions, ls)
		}
	}
	if len(boardSessions) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "No sessions found on that board"})
		return
	}

	// Update team status to sleeping (if a team exists for this board)
	if h.teamStore != nil {
		if team, err := h.teamStore.GetTeam(ctx, boardName); err == nil && team != nil && team.Status == "running" {
			h.teamStore.SetAllMembersStatus(ctx, team.ID, "active", "sleeping")
			h.teamStore.UpdateTeamStatus(ctx, team.ID, "sleeping")
		}
	}

	// Set all board sessions to sleeping
	affected, err := h.ss.SetBoardSleeping(ctx, boardName, true)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	// Pause the message board
	boardPaused := false
	if h.boardHandler != nil {
		h.boardHandler.SetPaused(boardName, true)
		boardPaused = true
	}

	// Kill tmux sessions for agents on this board
	killed := 0
	for _, ls := range boardSessions {
		err := h.terminal.KillSessionOnly(ctx, ls.AgentName, ls.AgentType, ls.SessionID)
		if err == nil {
			killed++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"sleeping":          true,
		"sessions_affected": affected,
		"sessions_killed":   killed,
		"board_paused":      boardPaused,
	})
}

// Wake wakes a sleeping team: relaunches sessions, clears sleeping, unpauses board.
// POST /api/sessions/live/team/{boardName}/wake
func (h *SessionsHandler) Wake(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	boardName := urlParam(r, "boardName")
	ctx := r.Context()

	// Find sleeping sessions on this board and relaunch
	allLive, _ := h.ss.GetAllLiveSessions(ctx)

	relaunched := 0
	for _, ls := range allLive {
		if ls.IsSleeping != 1 || ls.IsArchived == 1 || ls.BoardName == nil || *ls.BoardName != boardName {
			continue // archived agents stay asleep on team wake; they wake only one at a time
		}

		// Relaunch the session
		if err := h.wakeExistingSession(ctx, &ls); err != nil {
			log.Printf("Failed to wake session %s — keeping asleep: %v", ls.SessionID[:8], err)
			continue
		}
		relaunched++
	}

	// Clean up orphaned sleeping rows (duplicates where an awake version exists)
	if cleaned, err := h.ss.CleanupOrphanedSleeping(ctx); err == nil && cleaned > 0 {
		log.Printf("[wake] cleaned %d orphaned sleeping rows", cleaned)
	}

	// Update team status to running
	if relaunched > 0 && h.teamStore != nil {
		if team, err := h.teamStore.GetTeam(ctx, boardName); err == nil && team != nil && team.Status == "sleeping" {
			h.teamStore.WakeTeamMembers(ctx, team.ID)
			h.teamStore.UpdateTeamStatus(ctx, team.ID, "running")
		}
	}

	// Unpause board if at least one agent was woken
	boardPaused := true
	if relaunched > 0 && h.boardHandler != nil {
		h.boardHandler.SetPaused(boardName, false)
		boardPaused = false
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"sleeping":            false,
		"sessions_relaunched": relaunched,
		"board_paused":        boardPaused,
	})
}

// wakeExistingSession recreates the tmux/pty session for a sleeping agent
// using the EXISTING session ID. Preserves display name, board subscriptions, and history.
func (h *SessionsHandler) wakeExistingSession(ctx context.Context, ls *store.LiveSession) error {
	flags := store.UnmarshalFlags(ls.Flags)
	prompt := derefStrPtr(ls.Prompt)
	boardName := derefStrPtr(ls.BoardName)

	// Inject distilled knowledge on wake
	if boardName != "" {
		prompt = InjectKnowledgeIntoPrompt(h.cfg.CoralDir(), boardName, derefStrPtr(ls.DisplayName), prompt)
	}
	boardServer := derefStrPtr(ls.BoardServer)
	backend := "tmux"
	if ls.Backend != nil {
		backend = *ls.Backend
	}
	displayName := derefStrPtr(ls.DisplayName)
	boardType := derefStrPtr(ls.BoardType)
	_ = boardServer // retained for future use

	sessionName := naming.SessionName(ls.AgentType, ls.SessionID)
	logFile := naming.LogFile(h.cfg.LogDir, ls.AgentType, ls.SessionID)

	// A pty Coral holds every agent's terminal itself, whatever backend the agent last ran on
	// (#3634): an agent from a tmux run wakes the pty way (it used to go down the tmux path and
	// fail). If it still runs in Coral's old tmux server, waking it would start a second copy.
	if _, onPTY := h.backend.(*ptymanager.PTYBackend); onPTY {
		backend = "pty"
		if h.inOldTmux != nil && h.inOldTmux(ctx, sessionName) {
			return ErrStillInOldTmux
		}
	}

	agentImpl := agent.GetAgent(ls.AgentType)
	agent.TryPrepareResume(agentImpl, ls.SessionID, ls.WorkingDir)

	userSettings, _ := h.ss.GetSettings(ctx)
	cliPath := agent.ResolveCLIPath(userSettings, ls.AgentType)

	role := displayName
	if role == "" {
		role = ls.AgentType
	}

	launchParams := agent.LaunchParams{
		SessionID:       ls.SessionID,
		SessionName:     sessionName,
		ProtocolPath:    h.protocolPath(),
		ResumeSessionID: ls.SessionID,
		Flags:           flags,
		WorkingDir:      ls.WorkingDir,
		BoardName:       boardName,
		BoardLabel:      h.boardLabel(boardName),
		Role:            role,
		Prompt:          prompt,
		PromptOverrides: promptOverrides(userSettings),
		BoardType:       boardType,
		Capabilities:    nil,
		Tools:           store.UnmarshalFlags(ls.Tools),
		CLIPath:         cliPath,
		PermissionMode:  userSettings["default_permission_mode"],
		CoralDir:        h.cfg.CoralDir(),
		CoralHost:       h.cfg.AgentHost(),
		CoralPort:       h.cfg.Port,
	}

	// If the session is still running (e.g. it was marked sleeping by a false
	// crash detection rather than an actual crash), do NOT relaunch on top of
	// it: tmux new-session fails with "duplicate session" and, worse, the
	// resume command could be typed into the still-running agent's input.
	// Just clear the sleeping flag and re-subscribe below.
	alreadyRunning := false
	if backend == "pty" && h.backend != nil {
		alreadyRunning = h.backend.IsRunning(sessionName)
	} else if h.terminal != nil {
		alreadyRunning = h.terminal.HasSession(ctx, sessionName)
	}
	if alreadyRunning {
		log.Printf("[wake] session=%s is still running — skipping relaunch, clearing sleeping flag only", sessionName)
	}
	// The terminal is there but the agent CLI exited from it, leaving a plain shell (#3219):
	// resume the agent in that shell, as launch does, instead of only clearing the flag.
	if alreadyRunning && ls.AgentType != at.Terminal && h.terminal != nil {
		if exited, fg := ptymanager.AgentExited(ctx, h.terminal, sessionName, ls.AgentType, ls.SessionID); exited {
			cmd, err := launchCommand(agentImpl, launchParams)
			if err != nil {
				return err
			}
			target := sessionName + ".0"
			if backend == "pty" {
				target = sessionName
			}
			log.Printf("[wake] session=%s: the agent CLI had exited (%s in the foreground); resuming it in the same terminal", sessionName, fg)
			if err := sendLaunchCommand(ctx, h.terminal, target, cmd); err != nil {
				return err
			}
		}
	}

	if !alreadyRunning && ls.AgentType != at.Terminal {
		cmd, err := launchCommand(agentImpl, launchParams)
		if err != nil {
			return err
		}
		log.Printf("[wake] session=%s agent=%s backend=%s cmd=%s", sessionName, ls.AgentType, backend, cmd)

		if backend == "pty" && h.backend != nil {
			// PTY backend: spawn shell, then send command
			if err := h.backend.Spawn(sessionName, ls.AgentType, ls.WorkingDir, ls.SessionID, "", 200, 50); err != nil {
				return fmt.Errorf("pty spawn failed: %w", err)
			}
			if cmd != "" {
				if pb, ok := h.backend.(*ptymanager.PTYBackend); ok {
					if !pb.WaitReady(sessionName, 5*time.Second) {
						log.Printf("[launch] pty shell not ready after 5s, sending anyway: %s", sessionName)
					}
				} else {
					time.Sleep(500 * time.Millisecond)
				}
				if err := sendLaunchCommand(ctx, h.terminal, sessionName, cmd); err != nil {
					return fmt.Errorf("pty wake command failed: %w", err)
				}
			}
		} else {
			// tmux backend: create session with the EXISTING session name
			ptymanager.ResetAgentLog(logFile) // 0600, never through a link (#3601, W17)
			if err := h.terminal.CreateSession(ctx, sessionName, ls.WorkingDir); err != nil {
				return fmt.Errorf("create session: %w", err)
			}
			h.terminal.StartLogging(ctx, sessionName, logFile)

			folderName := filepath.Base(ls.WorkingDir)
			h.terminal.SetPaneTitle(ctx, sessionName+".0", fmt.Sprintf("%s — %s", folderName, ls.AgentType))

			if err := sendLaunchCommand(ctx, h.terminal, sessionName+".0", cmd); err != nil {
				return err
			}

			// Capture shell PID
			if tmuxTerm, ok := h.terminal.(*ptymanager.TmuxSessionTerminal); ok {
				if pid, err := tmuxTerm.Client().GetPanePID(ctx, sessionName); err == nil {
					h.ss.UpdateSessionPID(ctx, ls.SessionID, pid)
					log.Printf("[wake] captured PID %d for session %s", pid, sessionName)
				}
			}
		}
	}

	// Clear sleeping flag on existing row (no new DB row created)
	h.ss.SetSessionSleeping(ctx, ls.SessionID, false)

	// Re-subscribe to board with existing session name
	if boardName != "" {
		h.setupBoardAndPrompt(ls.SessionID, sessionName, ls.AgentType, boardName, displayName)
	}

	return nil
}

// ── Individual Session Sleep/Wake ────────────────────────────────────────

// SleepSession puts a single agent to sleep.
// POST /api/sessions/live/{sessionID}/sleep
func (h *SessionsHandler) SleepSession(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	sessionID := chi.URLParam(r, "sessionID")
	ctx := r.Context()

	sess, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil || sess == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Session not found"})
		return
	}

	h.ss.SetSessionSleeping(ctx, sessionID, true)

	// Kill tmux session to free resources
	if err := h.terminal.KillSessionOnly(ctx, sess.AgentName, sess.AgentType, sessionID); err != nil {
		log.Printf("Failed to kill tmux for session %s during sleep: %v", sessionID[:8], err)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sleeping": true})
}

// WakeSession wakes a single sleeping agent. Waking an archived agent also
// unarchives it: it is an explicit request for that one agent.
// POST /api/sessions/live/{sessionID}/wake
func (h *SessionsHandler) WakeSession(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	sessionID := chi.URLParam(r, "sessionID")
	ctx := r.Context()

	sess, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil || sess == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Session not found"})
		return
	}
	if sess.IsSleeping != 1 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Session is not sleeping"})
		return
	}

	if err := h.wakeExistingSession(ctx, sess); err != nil {
		log.Printf("Failed to wake session %s: %v", sessionID[:8], err)
		if errors.Is(err, ErrStillInOldTmux) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "code": "still_in_old_tmux"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Failed to relaunch session"})
		return
	}
	// One the Operator chose to wake from the over-the-cap list (#3636) gets Coral's notice.
	if h.takePendingAutoWake(sess.SessionID) {
		h.sendAutoWakeNotice(ctx, *sess)
	}

	// Unpause the board if this agent was on one
	bn := derefStrPtr(sess.BoardName)
	if bn != "" && h.boardHandler != nil {
		h.boardHandler.SetPaused(bn, false)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sleeping": false})
}

// ArchiveSession archives an agent: a running agent is put to sleep first
// (its terminal is killed), then marked archived. Archived agents are left out
// of Wake All, team wake and task assignment until unarchived or woken.
// POST /api/sessions/live/{sessionID}/archive
func (h *SessionsHandler) ArchiveSession(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	sessionID := chi.URLParam(r, "sessionID")
	ctx := r.Context()

	sess, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil || sess == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Session not found"})
		return
	}
	if sess.IsSleeping != 1 {
		if err := h.ss.SetSessionSleeping(ctx, sessionID, true); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if err := h.terminal.KillSessionOnly(ctx, sess.AgentName, sess.AgentType, sessionID); err != nil {
			log.Printf("Failed to kill tmux for session %s during archive: %v", sessionID[:min(8, len(sessionID))], err)
		}
	}
	if err := h.ss.SetSessionArchived(ctx, sessionID, true); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sleeping": true, "archived": true})
}

// UnarchiveSession returns an archived agent to plain sleeping. It does not
// start the agent; wake does that.
// POST /api/sessions/live/{sessionID}/unarchive
func (h *SessionsHandler) UnarchiveSession(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	sessionID := chi.URLParam(r, "sessionID")
	ctx := r.Context()

	sess, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil || sess == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Session not found"})
		return
	}
	if err := h.ss.SetSessionArchived(ctx, sessionID, false); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sleeping": sess.IsSleeping == 1, "archived": false})
}

// ── Bulk Sleep/Wake All ─────────────────────────────────────────────────

// SleepAll puts all active agents to sleep.
// POST /api/sessions/live/sleep-all
func (h *SessionsHandler) SleepAll(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	ctx := r.Context()
	allLive, _ := h.ss.GetAllLiveSessions(ctx)

	var active []store.LiveSession
	for _, ls := range allLive {
		if ls.IsSleeping != 1 {
			active = append(active, ls)
		}
	}
	if len(active) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions_affected": 0})
		return
	}

	boards := map[string]bool{}
	killed := 0
	for _, ls := range active {
		h.ss.SetSessionSleeping(ctx, ls.SessionID, true)
		if ls.BoardName != nil && *ls.BoardName != "" {
			boards[*ls.BoardName] = true
		}
		err := h.terminal.KillSessionOnly(ctx, ls.AgentName, ls.AgentType, ls.SessionID)
		if err == nil {
			killed++
		} else {
			log.Printf("Failed to kill tmux for session %s during sleep-all: %v", ls.SessionID[:8], err)
		}
	}

	// Pause all affected boards
	if h.boardHandler != nil {
		for b := range boards {
			h.boardHandler.SetPaused(b, true)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"sessions_affected": len(active),
		"sessions_killed":   killed,
	})
}

// WakeAll wakes all sleeping agents except archived ones.
// POST /api/sessions/live/wake-all
func (h *SessionsHandler) WakeAll(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	ctx := r.Context()
	allLive, _ := h.ss.GetAllLiveSessions(ctx)

	var sleeping []store.LiveSession
	for _, ls := range allLive {
		if ls.IsSleeping == 1 && ls.IsArchived == 0 { // archived agents are left asleep
			sleeping = append(sleeping, ls)
		}
	}
	if len(sleeping) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions_relaunched": 0})
		return
	}

	boards := map[string]bool{}
	relaunched := 0
	for _, ls := range sleeping {
		if err := h.wakeExistingSession(ctx, &ls); err != nil {
			log.Printf("Failed to wake session %s — keeping asleep: %v", ls.SessionID[:8], err)
			continue
		}
		relaunched++
		if bn := derefStrPtr(ls.BoardName); bn != "" {
			boards[bn] = true
		}
	}

	// Unpause affected boards
	if h.boardHandler != nil {
		for b := range boards {
			h.boardHandler.SetPaused(b, false)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions_relaunched": relaunched})
}

// ResolveByPIDs looks up the agent identity for a set of process IDs.
// Used by coral-board to identify itself when env vars are stripped (e.g. Codex sandbox).
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
	ls, err := h.ss.ResolveByPIDs(r.Context(), pids)
	if err != nil {
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
		"session_name":  ls.SessionID,
	})
}

// knownContextWindow returns zero when model metadata is absent or unrecognized,
// preventing payload builders from publishing a percentage against stale data.
func knownContextWindow(model *string) int {
	if model == nil {
		return 0
	}
	return proxy.LookupContextWindow(*model)
}

func addContextUsage(entry map[string]any, model *string, turnContext int) {
	entry["context_window"] = nil
	entry["context_pct"] = nil
	window := knownContextWindow(model)
	if window == 0 {
		return
	}
	entry["context_window"] = window
	if turnContext <= 0 {
		return
	}
	pct := int(float64(turnContext) / float64(window) * 100)
	if pct > 100 {
		pct = 100
	}
	entry["context_pct"] = pct
}

func stripAgentPermissionFlags(flags []string) []string {
	agentPermFlags := map[string]bool{
		"--full-auto": true, "--dangerously-skip-permissions": true,
		"--dangerously-bypass-approvals-and-sandbox": true,
		"--sandbox": true, "--search": true,
		"--approval-mode": true, "--permission-mode": true, "--yolo": true,
	}

	var cleanFlags []string
	skipNext := false
	for _, f := range flags {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(f, "--permission-mode=") {
			continue
		}
		if agentPermFlags[f] {
			// Some flags take a value argument (e.g. --sandbox workspace-write, -a untrusted).
			if f == "--sandbox" || f == "--approval-mode" || f == "--permission-mode" || f == "-a" {
				skipNext = true
			}
			continue
		}
		if f == "-a" {
			skipNext = true
			continue
		}
		cleanFlags = append(cleanFlags, f)
	}
	return cleanFlags
}

// launchBackendLabel reports the terminal backend for analytics as a closed
// enum: the requested one when valid, otherwise the one this server runs.
func (h *SessionsHandler) launchBackendLabel(requested string) string {
	switch requested {
	case "tmux", "pty":
		return requested
	}
	switch h.terminal.(type) {
	case *ptymanager.TmuxSessionTerminal:
		return "tmux"
	case *ptymanager.PTYSessionTerminal:
		return "pty"
	}
	return "unknown"
}

// classifyLaunchError maps a launch error to a controlled analytics category.
// It inspects the message only to choose a category; the text is never sent.
func classifyLaunchError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "cli not found"):
		return tracking.FailProviderUnavailable
	case strings.Contains(msg, "tmux is required") || strings.Contains(msg, "terminal backend"):
		return tracking.FailBackendUnavailable
	case strings.Contains(msg, "directory not found") || strings.Contains(msg, "no such file") || strings.Contains(msg, "not a directory") || strings.Contains(msg, "working directory"):
		return tracking.FailWorkdirUnavailable
	case strings.Contains(msg, "spawn failed") || strings.Contains(msg, "launch command failed") || strings.Contains(msg, "launch command delivery") || strings.Contains(msg, "new-session failed"):
		return tracking.FailSpawnFailed
	}
	return tracking.FailInternal
}

// launchDirError is the error for a launch working directory that is not an
// existing directory, or "" when it is usable.
func launchDirError(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil || !isDir(abs) {
		return fmt.Sprintf("directory not found: %s", dir)
	}
	return ""
}

// sleepReason is why Coral put a sleeping agent to sleep (#3344): "terminal_gone" when its
// terminal session no longer exists, "" when the user (or a team action) did.
func sleepReason(ls store.LiveSession) string {
	if ls.SleepReason == nil {
		return ""
	}
	return *ls.SleepReason
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

// boardLabel is a board's display name for an agent's prompt (#3536); "" if unknown.
func (h *SessionsHandler) boardLabel(key string) string {
	if h.bs == nil || key == "" {
		return ""
	}
	return h.bs.BoardLabel(context.Background(), key)
}

var launchBoardMu sync.Mutex

// launchBoard decides the board a launch uses (#3536, the operator's decision): board_id
// joins that board; board_name starts a NEW board (a fresh b- id, that label) unless a board
// with that id or label already exists, which is answered 409 board_name_exists with the
// candidates (join one by its board_id, or send new_board to start another with the same
// name). A new board's name follows the #3534 rule. It returns the board's id, or false
// after answering.
func (h *SessionsHandler) launchBoard(w http.ResponseWriter, r *http.Request, boardID, boardName string, newBoard bool) (string, bool) {
	ctx := r.Context()
	if h.bs == nil {
		if boardID != "" {
			return boardID, true
		}
		return boardName, true
	}
	if boardID != "" {
		b, err := h.bs.GetBoard(ctx, boardID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "board_not_found", "code": "board_not_found", "message": "No board has that id"})
			return "", false
		}
		return b.ID, true
	}
	boardName = strings.TrimSpace(boardName)
	// One check-then-create at a time, so two launches of the same new name can't both see
	// it free and start two boards with one label.
	launchBoardMu.Lock()
	defer launchBoardMu.Unlock()
	var existing []board.Board
	if b, err := h.bs.GetBoard(ctx, boardName); err == nil {
		if inUse, _ := h.bs.BoardExists(ctx, b.ID); inUse {
			existing = append(existing, *b)
		}
	} else if inUse, _ := h.bs.BoardExists(ctx, boardName); inUse {
		existing = append(existing, board.Board{ID: boardName, Label: boardName})
	}
	if named, err := h.bs.BoardsLabelledForLaunch(ctx, boardName); err == nil {
		for _, b := range named {
			if b.ID != boardName {
				existing = append(existing, b)
			}
		}
	}
	// Another board with a name that's taken makes that name ambiguous for every caller who
	// uses it: the Operator's choice (Security's #3536 review). An agent's token is refused
	// with strict local auth, and recorded otherwise.
	if len(existing) > 0 && newBoard && h.agentOperatorGate != nil && h.agentOperatorGate(r, "POST /api/sessions/launch new_board") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden_for_agent", "code": "forbidden_for_agent",
			"message": "An agent can't start another board with a name that's taken: that is for the Operator. Join it with board_id, or pick a new name"})
		return "", false
	}
	if len(existing) > 0 && !newBoard {
		// Next to the member count, how many of the board's agents are running (#3543).
		cands := boardCandidates(r, h.bs, existing)
		live := h.liveSessionNames(ctx)
		for _, c := range cands {
			c["live_agents"] = h.liveAgentsOn(ctx, c["board_id"].(string), live)
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "board_name_exists", "code": "board_name_exists",
			"message":    "A board named \"" + boardName + "\" already exists: join it (board_id) or start another (new_board)",
			"candidates": cands})
		return "", false
	}
	if problem := h.bs.LabelProblem(boardName); problem != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_board_name", "code": "invalid_board_name", "message": problem})
		return "", false
	}
	b, err := h.bs.CreateBoard(ctx, boardName, "Operator")
	if err != nil {
		errInternalServer(w, err.Error())
		return "", false
	}
	return b.ID, true
}

// addBoardLabels gives every session row with a board ("board_project") its board's display
// name as "board_label" (#3536), with one lookup for the whole list.
func (h *SessionsHandler) addBoardLabels(ctx context.Context, rows []map[string]any) {
	if h.bs == nil {
		return
	}
	var ids []string
	for _, row := range rows {
		if id := boardKeyOf(row["board_project"]); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	labels := h.bs.BoardLabels(ctx, ids)
	for _, row := range rows {
		if id := boardKeyOf(row["board_project"]); id != "" {
			row["board_label"] = labels[id]
		}
	}
}

func boardKeyOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case *string:
		if t != nil {
			return *t
		}
	}
	return ""
}

// liveSessionNames is the set of session names of every live (running or sleeping) session.
func (h *SessionsHandler) liveSessionNames(ctx context.Context) map[string]bool {
	live := map[string]bool{}
	if h.ss == nil {
		return live
	}
	sessions, err := h.ss.GetAllLiveSessions(ctx)
	if err != nil {
		return live
	}
	for _, ls := range sessions {
		live[naming.SessionName(ls.AgentType, ls.SessionID)] = true
	}
	return live
}

// uniqueBoardRole returns displayName, or "<role> N" (the lowest N from 2) when another
// live agent already holds that role on the board (#3543). A subscription left by an agent
// that is no longer running doesn't count: a relaunched or resurrected member keeps its name.
//
// Names picked in the last two minutes count as taken too (roleReservations): the board
// subscription is written only after the launch, so two launches of one role close together
// (two tabs, a double click) would otherwise both see the role free.
func (h *SessionsHandler) uniqueBoardRole(ctx context.Context, boardName, displayName, agentType string) string {
	if h.bs == nil {
		return displayName
	}
	h.roleRes.mu.Lock()
	defer h.roleRes.mu.Unlock()
	if h.roleRes.at == nil {
		h.roleRes.at = map[string]time.Time{}
	}
	now := time.Now()
	taken := map[string]bool{}
	for key, at := range h.roleRes.at {
		if now.Sub(at) > roleReservationTTL {
			delete(h.roleRes.at, key)
			continue
		}
		if b, name, ok := strings.Cut(key, "\x00"); ok && b == boardName {
			taken[name] = true
		}
	}
	if subs, err := h.bs.ListSubscribers(ctx, boardName); err == nil && len(subs) > 0 {
		live := h.liveSessionNames(ctx)
		for _, s := range subs {
			if s.SessionName != "" && live[s.SessionName] {
				taken[strings.ToLower(s.SubscriberID)] = true
			}
		}
	}
	role := naming.SubscriberID(displayName, agentType)
	pick := displayName
	if taken[strings.ToLower(role)] {
		// A taken numbered name is renumbered from its base role: "Backend Dev 2" becomes
		// "Backend Dev 3", not "Backend Dev 2 2" (QA's #3543 review).
		base := role
		if i := strings.LastIndexByte(role, ' '); i > 0 {
			if _, err := strconv.Atoi(role[i+1:]); err == nil {
				base = role[:i]
			}
		}
		for n := 2; n < 1000; n++ {
			if candidate := fmt.Sprintf("%s %d", base, n); !taken[strings.ToLower(candidate)] {
				pick = candidate
				break
			}
		}
	}
	h.roleRes.at[boardName+"\x00"+strings.ToLower(naming.SubscriberID(pick, agentType))] = now
	return pick
}

// roleReservations holds the names uniqueBoardRole picked recently, per board; one per
// handler (QA's #3543 review: a package-level map leaked between tests).
type roleReservations struct {
	mu sync.Mutex
	at map[string]time.Time
}

const roleReservationTTL = 2 * time.Minute

// releaseRoleReservation drops a name's reservation: its subscription now holds it, or its
// launch failed.
func (h *SessionsHandler) releaseRoleReservation(boardName, role string) {
	h.roleRes.mu.Lock()
	delete(h.roleRes.at, boardName+"\x00"+strings.ToLower(role))
	h.roleRes.mu.Unlock()
}

// liveAgentsOn counts the board's subscribers whose session is live.
func (h *SessionsHandler) liveAgentsOn(ctx context.Context, boardID string, live map[string]bool) int {
	subs, err := h.bs.ListSubscribers(ctx, boardID)
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range subs {
		if s.SessionName != "" && live[s.SessionName] {
			n++
		}
	}
	return n
}

// launchedName is the display name launchSession gave the agent (numbered when its role was
// taken on the board, #3543), else the one asked for.
func launchedName(result map[string]any, asked string) string {
	if n, _ := result["display_name"].(string); n != "" {
		return n
	}
	return asked
}

// diffPathRefused applies, for anything but this computer's own dashboard (Coral Hub, an API
// key, a remote client), the same bounds as file-content to a file a diff or its base shows
// (#3558, Security's #3556 finding 1; #3596 W2): a root that isn't the filesystem or the home
// folder, no sensitive path, no dot-file or dot-folder (.env.local, .git/config), inside the
// working tree once resolved, and one file, not a folder's worth (QA's #3558 review). Refused
// files look exactly like missing ones.
func diffPathRefused(workdir, fp, fullPath string) bool {
	real := realOrSelf(fullPath)
	if !rootAllowed(realPath(workdir)) || diffPathHidden(fp) || sensitivePath(real) ||
		!isPathWithinDir(realOrSelf(workdir), real) {
		return true
	}
	if rel, err := filepath.Rel(realOrSelf(workdir), real); err != nil || diffPathHidden(rel) {
		return true
	}
	if fi, err := os.Stat(real); err == nil && fi.IsDir() {
		return true
	}
	return false
}

// launchCommand is the agent's launch command with the bundled tools on PATH. It fails when
// the agent's prompt, settings or instructions files can't be written to the temp folder: the
// launch fails with that error instead of starting an agent without them (#3601 W12).
func launchCommand(ag agent.Agent, p agent.LaunchParams) (string, error) {
	cmd, err := ag.LaunchCommand(p)
	if err != nil {
		return "", err
	}
	return agent.WrapWithBundlePath(cmd), nil
}

// ErrStillInOldTmux is a wake on a pty Coral of an agent still running in Coral's old tmux
// server (#3634): ending it first (Settings, or the Agents tab's banner) avoids a second copy.
var ErrStillInOldTmux = errors.New("this agent is still running in Coral's old tmux server; end the agents left there first, then wake it")
