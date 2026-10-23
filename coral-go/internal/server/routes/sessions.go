// Package routes implements HTTP handlers for the Coral API.
package routes

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/logparse"
	"github.com/subgentic/coral/internal/machinehealth"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/proxy"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/tmux"
)

// SessionsHandler handles all live session API endpoints.
type SessionsHandler struct {
	// artifactNudge reminds an agent that published a claude.ai artifact to make a Coral one (#6583); nil: never.
	artifactNudge *ClaudeArtifactNudger
	// indexKick asks the history indexer for a pass soon (set by SetIndexKick): an agent's reply has just been written to its transcript (#6198).
	indexKick func()
	// newUUID, when set, makes this handler's new session ids (a test that compares two handlers gives both the same ones without
	// touching the package variable, so such tests can run in parallel, #4982); nil is generateUUID.
	newUUID func() string
	// boardStatus shares one computation of a board's status among the requests that arrive
	// together (#3721); it keeps nothing, so a status is never stale.
	boardStatus *replyCache
	// sendGate lets a person's messages through to each agent one at a time (#4587, human_send.go).
	sendGate humanSendGate
	// modelSwitches tracks the live model and thinking-level switches in progress (#4969, model_switch.go).
	modelSwitches modelSwitchTracker
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
	// healthOn is the last health state read of every team, for the rows when a read fails (#5119).
	healthOn healthOnMemo
	// commentOwner names the owner on new artifact comments (nil: this computer's user, #5176); commentLimit is the per-author write rate limit.
	commentOwner commentOwnerIdentity
	commentLimit commentLimiter
	sendLimit    sendLimiter // Send to agent: asks per owner per hour (#6443)
	// wsHeartbeat overrides wsHeartbeatInterval when set (nanoseconds; tests).
	wsHeartbeat  atomic.Int64
	remoteAgents background.RemoteAgentSource
	// remoteAfter is the timer of the feed's remote-change debounce (nil: time.After); a test supplies its own to fire the window itself, not by waiting for it (#5942).
	remoteAfter func(time.Duration) <-chan time.Time
	db          *store.DB
	ss          *store.SessionStore
	ts          *store.TaskStore
	subagents   *store.SubagentStore
	gs          *store.GitStore
	bs          *board.Store
	cfg         *config.Config
	terminal    ptymanager.SessionTerminal
	jsonl       *jsonl.SessionReader
	backend     ptymanager.TerminalBackend // nil = use tmux directly

	boardHandler  *BoardHandler          // for sleep/wake board pausing
	schedStore    *store.ScheduleStore   // for active runs in websocket
	notifications *NotificationStore     // for UI notifications via websocket
	teamStore     *store.TeamStore       // for team persistence
	tokenStore    *store.TokenUsageStore // for token usage tracking

	goals GoalRequester // nil until the goal generator starts

	machineHealth MachineHealthSource // nil until StartMachineHealth (#3444)

	pending   pendingTools  // tool calls started but not finished (PreToolUse hook)
	readiness taskReadiness // one "did this finish unblock others" check at a time per task (#4602, L5)
	hints     promptHints   // whether a prompt may be open on a session's terminal (its latest hook events), for the chat reply
	// loginScreens: whether a Claude agent with no transcript yet shows its login screen (#3830).
	loginScreens loginScreens
	// startupScreens: whether an agent that has sent no event yet waits on a prompt its CLI drew at launch (#4543).
	startupScreens loginScreens
	// screens: captures the read-only pollers (the agent poll) share for a moment (#3887)
	screens screenSnaps
	// hooksRev is the hook set each session's agent loaded at its launch, from its events
	// (session id → revision, #3655 F3). In memory: until a session's next event after a
	// restart it is unknown, and the gate takes the screen rule (the safe side).
	hooksRev sync.Map
	// liveModes is the permission mode each Claude session's CLI reported with its latest hook event (session id → one of the six words, live_permission_mode.go). In memory: until a session's
	// next event after a restart it is unknown and the dashboard reads the CLI's mode line instead.
	liveModes sync.Map
	// hooksNoRev: sessions that sent an event without a revision, for the "out of date" log.
	hooksNoRev sync.Map
	// hooksRefreshing: sessions with a refresh-hooks in progress, so a second request at once
	// gets 409 instead of a second sleep and wake (#3685). onRefreshHeld (tests only) runs while
	// a refresh holds its session.
	hooksRefreshing sync.Map
	onRefreshHeld   func(sessionID string)

	autoAccept *AutoAcceptor // answers approval prompts for auto-accept sessions (auto_accept.go)

	// autoAcceptOrder orders an enable's store write against a disable-all and a per-session Stop (auto_accept.go). autoAcceptStops counts both; an enable that began
	// before one must not turn the run on after it. The Cloud Hub sends a disable-all the moment an owner lowers a machine's access and does not wait for an enable that is still deciding.
	autoAcceptOrder sync.Mutex
	autoAcceptStops atomic.Uint64
	// onAutoEnableDeciding (tests only) runs after an enable passed its checks and before its write, the window the order closes.
	onAutoEnableDeciding func()

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
	// restarting: the sessions with a restart or resume running (beginRestart, #5213).
	restarting sync.Map
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
	if err := decodeJSON(r, &body); err != nil {
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
		errConflict(w, "goal generation is not running")
		return
	}
	if settings, err := h.ss.GetSettings(r.Context()); err == nil && background.GoalsDisabled(settings) {
		errConflict(w, "goal generation is turned off")
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
	return setreg.Enum(settings, setreg.GitDiffMode)
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
	model := setreg.TextFamily(settings, setreg.DefaultModelPrefix+agentType)
	if model == "" {
		if agentType == at.Agy || agentType == at.Antigravity {
			model = setreg.TextFamily(settings, setreg.DefaultModelPrefix+at.Gemini)
		} else if agentType == at.Gemini {
			model = setreg.TextFamily(settings, setreg.DefaultModelPrefix+at.Agy)
		}
	}
	return model
}

// NewSessionsHandler creates a SessionsHandler with the given dependencies.
func NewSessionsHandler(db *store.DB, cfg *config.Config, backend ptymanager.TerminalBackend, terminal ptymanager.SessionTerminal, bs *board.Store) *SessionsHandler {
	return &SessionsHandler{
		autoWake:       autoWaker{sleep: background.SleepCtx, now: time.Now},
		exits:          exitWatch{after: defaultAfter},
		startupScreens: loginScreens{stampDecides: true},
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
		defer safego.Recover("server/routes/sessions:387")
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
		defer safego.Recover("server/routes/sessions:410")
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
	// The list paths only show agents (the session list, the team view): the shared pane list
	// may be a few seconds old (#4346); a launch or a kill from Coral still shows at once.
	panes, err := h.terminal.ListSessions(tmux.WithPaneListMaxAge(ctx.Context(), tmux.DisplayPaneListAge))
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

// generateUUID is a new session's id (random; tests that compare two runs give both the same ids).
func (h *SessionsHandler) uuid() string {
	if h.newUUID != nil {
		return h.newUUID()
	}
	return generateUUID()
}

var generateUUID = func() string {
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

// ErrStillInOldTmux is a wake on a pty Coral of an agent still running in Coral's old tmux
// server (#3634): ending it first (Settings, or the Agents tab's banner) avoids a second copy.
var ErrStillInOldTmux = errors.New("this agent is still running in Coral's old tmux server; end the agents left there first, then wake it")
