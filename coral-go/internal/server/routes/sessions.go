// Package routes implements HTTP handlers for the Coral API.
package routes

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cdknorow/coral/internal/testguard"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

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
	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/safefs"
	"github.com/cdknorow/coral/internal/sessionstate"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
	"github.com/cdknorow/coral/internal/tracking"
)

// SessionsHandler handles all live session API endpoints.
type SessionsHandler struct {
	// boardStatus shares one computation of a board's status among the requests that arrive
	// together (#3721); it keeps nothing, so a status is never stale.
	boardStatus *replyCache
	// inOldTmux reports an agent session still running in Coral's private tmux server, for a
	// pty Coral's wake (#3634). Tests replace it.
	inOldTmux func(ctx context.Context, sessionName string) bool
	// autoWake is the start's wake of the agents running when a pty Coral stopped (#3636).
	autoWake autoWaker
	// exits is the notice and auto-resume for agents that died on their own (#3613).
	exits exitWatch
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
	hints   promptHints  // whether a prompt may be open on a session's terminal (its latest hook events), for the chat reply
	// loginScreens: whether a Claude agent with no transcript yet shows its login screen (#3830).
	loginScreens loginScreens
	// screens: captures the read-only pollers (the agent poll) share for a moment (#3887)
	screens screenSnaps
	// hooksRev is the hook set each session's agent loaded at its launch, from its events
	// (session id → revision, #3655 F3). In memory: until a session's next event after a
	// restart it is unknown, and the gate takes the screen rule (the safe side).
	hooksRev sync.Map
	// hooksNoRev: sessions that sent an event without a revision, for the "out of date" log.
	hooksNoRev sync.Map
	// hooksRefreshing: sessions with a refresh-hooks in progress, so a second request at once
	// gets 409 instead of a second sleep and wake (#3685). onRefreshHeld (tests only) runs while
	// a refresh holds its session.
	hooksRefreshing sync.Map
	onRefreshHeld   func(sessionID string)

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
	tail    *logTail // the log's end as last read: a grown log reads only its new bytes (#3711)
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
		autoWake: autoWaker{sleep: background.SleepCtx, now: time.Now},
		exits:    exitWatch{after: defaultAfter},
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
		boardStatus:       newReplyCache(0),
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
	var prev *logTail
	if cached, ok := h.logStatusCache[path]; ok {
		prev = cached.tail
	}
	h.logStatusMu.Unlock()
	value, tail := readLogTail(path, prev)
	if tail != nil {
		info = tail.info // the stat the status was read with
	}
	h.logStatusMu.Lock()
	if len(h.logStatusCache) >= 512 {
		for key := range h.logStatusCache {
			delete(h.logStatusCache, key)
			break
		}
	}
	h.logStatusCache[path] = cachedLogStatus{info: info, size: info.Size(), modTime: info.ModTime(), value: cloneLogStatus(value), tail: tail}
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
	status, _ := readLogTail(logPath, nil)
	return status
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
	value := ptr.Value(ls.ResumeFromID)
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
	return ptr.Value(session.Prompt)
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
	if existing, err := h.ts.FindOpenAgentTask(r.Context(), name, body.Title, ptr.NonEmpty(body.SessionID)); err == nil && existing != nil {
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
		deps, err = parseBlockedBy(body.BlockedBy, store.AgentTaskProject(name, ptr.NonEmpty(body.SessionID)))
		if err != nil {
			errBadRequest(w, err.Error())
			return
		}
	}
	task, err := h.ts.CreateAgentTaskWithWorkflow(r.Context(), name, body.Title, ptr.NonEmpty(body.SessionID), displayName, body.Body, body.Priority, &board.CreateTaskOpts{Workflow: body.Workflow, BlockedBy: deps, Draft: body.Draft, MaxDepth: 32})
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
	return itemScope{name: chi.URLParam(r, "name"), sid: ptr.NonEmpty(claimed)}, true
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
	note, err := h.ts.CreateAgentNote(r.Context(), name, body.Content, ptr.NonEmpty(body.SessionID))
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
		HooksRev   int    `json:"hooks_rev"`
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
	if hasToken && body.HooksRev > 0 {
		_ = h.ss.SetHooksRev(r.Context(), body.SessionID, body.HooksRev) // kept across restarts (#3665)
		// The agent's own hooks say which set they are. Only a revision is stored: an event
		// without one (an older set, or an old Coral hook left in the user's global Claude
		// settings next to the current ones) must never turn a current set back (Security's
		// SHOULD on #3655: flapping would spoil the shadow data).
		h.hooksRev.Store(body.SessionID, body.HooksRev)
	} else if hasToken && body.EventType != "goal" {
		h.hooksNoRev.Store(body.SessionID, true) // for the "out of date" log only
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
			if err := h.startAgentCLI(ctx, boardName, sessionName, cmd); err != nil {
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
			if err := h.startAgentCLI(ctx, boardName, sessionName+".0", cmd); err != nil {
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
		DisplayName:   ptr.NonEmpty(displayName),
		ResumeFromID:  ptr.NonEmpty(resumeSessionID),
		Flags:         store.MarshalFlags(flags),
		Prompt:        ptr.NonEmpty(prompt),
		BoardName:     ptr.NonEmpty(boardName),
		BoardServer:   ptr.NonEmpty(boardServer),
		Backend:       ptr.NonEmpty(backend),
		BoardType:     ptr.NonEmpty(boardType),
		Capabilities:  store.MarshalCapabilities(capabilities),
		Model:         ptr.NonEmpty(model),
		ContextWindow: proxy.LookupContextWindow(model),
		Tools:         store.MarshalCapabilities(tools),
		PID:           shellPID,
	})
	h.noteLaunchedHooks(ctx, sessionID, agentType) // #3665
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
		if humanInputCaller(r) {
			h.resetAutoResume(ls.SessionID) // a person's wake (#3613, Security's SHOULD 1)
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
func (h *SessionsHandler) wakeExistingSession(ctx context.Context, ls *store.LiveSession) (err error) {
	launched := false // the agent CLI was started again: it loaded the current hook set
	defer func() {
		if err == nil && launched {
			h.noteLaunchedHooks(ctx, ls.SessionID, ls.AgentType)
		}
	}()
	flags := store.UnmarshalFlags(ls.Flags)
	prompt := ptr.Value(ls.Prompt)
	boardName := ptr.Value(ls.BoardName)

	// Inject distilled knowledge on wake
	if boardName != "" {
		prompt = InjectKnowledgeIntoPrompt(h.cfg.CoralDir(), boardName, ptr.Value(ls.DisplayName), prompt)
	}
	boardServer := ptr.Value(ls.BoardServer)
	backend := "tmux"
	if ls.Backend != nil {
		backend = *ls.Backend
	}
	displayName := ptr.Value(ls.DisplayName)
	boardType := ptr.Value(ls.BoardType)
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
			launched = true
			target := sessionName + ".0"
			if backend == "pty" {
				target = sessionName
			}
			log.Printf("[wake] session=%s: the agent CLI had exited (%s in the foreground); resuming it in the same terminal", sessionName, fg)
			if err := h.startAgentCLI(ctx, boardName, target, cmd); err != nil {
				return err
			}
		}
	}

	if !alreadyRunning && ls.AgentType != at.Terminal {
		cmd, err := launchCommand(agentImpl, launchParams)
		if err != nil {
			return err
		}
		launched = true
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
				if err := h.startAgentCLI(ctx, boardName, sessionName, cmd); err != nil {
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

			if err := h.startAgentCLI(ctx, boardName, sessionName+".0", cmd); err != nil {
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
	// A person's wake (never an agent's) lets Coral auto-resume it again (#3613, Security's SHOULD 1).
	if humanInputCaller(r) {
		h.resetAutoResume(sess.SessionID)
	}
	// One the Operator chose to wake from the over-the-cap list (#3636) gets Coral's notice.
	if h.takePendingAutoWake(sess.SessionID) {
		h.sendAutoWakeNotice(ctx, *sess)
	}

	// Unpause the board if this agent was on one
	bn := ptr.Value(sess.BoardName)
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
		if humanInputCaller(r) {
			h.resetAutoResume(ls.SessionID) // a person's wake (#3613, Security's SHOULD 1)
		}
		relaunched++
		if bn := ptr.Value(ls.BoardName); bn != "" {
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
	// A network path is refused before anything stats it: on Windows the stat alone connects to
	// the host as the user (#3671).
	if safefs.RefuseRemote(dir) != nil {
		return fmt.Sprintf("a network path can't be a working folder: %s", dir)
	}
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
