// Package routes implements HTTP handlers for the Coral API.
package routes

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/jsonl"
	"github.com/cdknorow/coral/internal/logparse"
	"github.com/cdknorow/coral/internal/machinehealth"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
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

// ErrStillInOldTmux is a wake on a pty Coral of an agent still running in Coral's old tmux
// server (#3634): ending it first (Settings, or the Agents tab's banner) avoids a second copy.
var ErrStillInOldTmux = errors.New("this agent is still running in Coral's old tmux server; end the agents left there first, then wake it")
