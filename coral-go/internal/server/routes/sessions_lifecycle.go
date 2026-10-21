package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/executil"
	"log"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentmodels"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/logparse"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/proxy"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
)

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
	decodeJSON(r, &body)
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
				defer safego.Recover("server/routes/sessions_lifecycle:164")
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

// putSubscriptionBack returns a board subscription to the session it was on, when a launch failed
// after the subscription had moved to the session that was to start (restart and resume move it
// first, so the new process's first request is a member's). sub is the subscription as it was
// before the move; nil is nothing to put back.
func (h *SessionsHandler) putSubscriptionBack(ctx context.Context, sub *board.Subscriber) {
	if sub == nil || h.bs == nil {
		return
	}
	if _, err := h.bs.Subscribe(ctx, sub.Project, sub.SubscriberID, sub.JobTitle, sub.SessionName,
		sub.OriginServer, sub.ReceiveMode, sub.CanPeek == 1); err != nil {
		log.Printf("[launch] putting %s back on its board %s after a failed launch: %v", sub.SessionName, sub.Project, err)
		return
	}
	writeBoardStateFile(sub.SessionName, sub.Project, sub.JobTitle, h.cfg)
}

func sendLaunchCommand(ctx context.Context, terminal ptymanager.SessionTerminal, target, command string) error {
	if err := terminal.SendToTarget(ctx, target, command); err != nil {
		return fmt.Errorf("launch command delivery to %s failed: %w", target, err)
	}
	return nil
}

// lifecycleTimeout bounds the part of a restart, resume or wake that is past the point where it can be undone (a var for the tests).
var lifecycleTimeout = 2 * time.Minute

// lifecycleCtx is the context for the part of a restart, resume or wake that cannot be left half done (#5171): once the old terminal is ended or the new
// one made, the rest (the pane respawned and renamed, the log started, the start command typed, the store updated) must run to its end whether or not
// the dashboard is still waiting. A request that is dropped there (a reload, a tab change) used to leave the agent in neither state: the old one
// ended, the new one never started, a bare shell. Detached from the request, with its own bound, it is the old agent or the new one, never between.
func lifecycleCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return lifecycleCtxN(ctx, 0)
}

// lifecyclePerSession is what each session of a wake of many, or a team launch, adds to the bound (a var for the tests).
var lifecyclePerSession = 5 * time.Second

// lifecycleCtxN is lifecycleCtx for an action that works on n sessions one after the other: the bound grows with n, so the last of a large team is not cut off.
func lifecycleCtxN(ctx context.Context, n int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), lifecycleTimeout+time.Duration(n)*lifecyclePerSession)
}

type tunneledKey struct{}

// viaHub marks ctx with whether the request came through Coral Hub, for answers that must not name this computer's paths.
func viaHub(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, tunneledKey{}, cloud.Tunneled(r))
}

// cliPathRefusal is the 422 answer for a configured CLI path that can't be run (#115): the setting and the path, and through Coral Hub the
// setting only, as the CLI check says it. ok is false for any other error.
func cliPathRefusal(ctx context.Context, err error) (answer, bool) {
	var cpe *agent.CLIPathError
	if !errors.As(err, &cpe) {
		return nil, false
	}
	msg := cpe.Error()
	if hub, _ := ctx.Value(tunneledKey{}).(bool); hub {
		msg = cpe.HubText()
	}
	return func(w http.ResponseWriter) { writeError(w, http.StatusUnprocessableEntity, "cli_path_unusable", msg) }, true
}

// launchErrText is a launch error's text for an answer: a configured CLI path that can't be run names the setting only when the request came
// through Coral Hub (#115); any other error is its own text.
func launchErrText(ctx context.Context, err error) string {
	var cpe *agent.CLIPathError
	if hub, _ := ctx.Value(tunneledKey{}).(bool); hub && errors.As(err, &cpe) {
		return cpe.HubText()
	}
	return err.Error()
}

// beginRestart takes the session's turn (h.restarting: the sessions with a restart or resume running now, by name) and says whether it was free; the caller
// ends it with the returned func. A second request for one of them (a double click, a client that retries while the first still runs, now that the first
// no longer ends with its request) is refused, so one agent starts, not two (#5213).
func (h *SessionsHandler) beginRestart(name string) (end func(), ok bool) {
	if _, busy := h.restarting.LoadOrStore(name, struct{}{}); busy {
		return nil, false
	}
	return func() { h.restarting.Delete(name) }, true
}

func refuseRestartBusy(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "this agent is already being restarted; wait for it to finish", "in_progress": true})
}

// afterTeamSlot runs f once d has passed (tests replace it).
var afterTeamSlot = func(d time.Duration, f func()) { time.AfterFunc(d, f) }

// startAgentCLI types an agent's start command (its CLI, or its resume) into its terminal. The
// starts of one team are 2-3 s apart (#3613: each starts an API turn at once): the team's first
// is typed now; a later one is typed when its slot comes, on a background context, and this
// returns at once. A launch, team launch or wake request never waits for the spacing, so the
// hub's response deadline can't cut a team launch in half (Security's MUST); the terminal and
// the session already exist, and the dashboard shows each agent start.
func (h *SessionsHandler) startAgentCLI(ctx context.Context, team, target, cmd string) error {
	d := ptymanager.ReserveTeamStart(team)
	if d <= 0 {
		return sendLaunchCommand(ctx, h.terminal, target, cmd)
	}
	log.Printf("[launch] %s: its start is typed in %s (the team's starts are spaced)", target, d.Round(100*time.Millisecond))
	afterTeamSlot(d, func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := sendLaunchCommand(bg, h.terminal, target, cmd); err != nil {
			log.Printf("[launch] %s: the spaced start failed: %v", target, err)
		}
	})
	return nil
}

// restartRequest is a restart's body: what the dashboard's restart dialog may change.
type restartRequest struct {
	AgentType    string              `json:"agent_type"`
	ExtraFlags   string              `json:"extra_flags"`
	SessionID    string              `json:"session_id"`
	Prompt       string              `json:"prompt"`
	Model        *string             `json:"model"`
	Thinking     *string             `json:"thinking"` // the thinking level to restart with: nil keeps the stored one, "" clears it (#4969)
	Capabilities *agent.Capabilities `json:"capabilities"`
}

// answer writes a refused or failed request's reply; a nil answer is success so far.
type answer func(w http.ResponseWriter)

// restartPlan is a restart worked out before anything is touched: the agent, where it runs, its new identity and
// the settings it starts with.
type restartPlan struct {
	req          restartRequest
	agentType    string
	stored       *store.LiveSession // the session being restarted (nil: unknown to the store)
	subscription *board.Subscriber  // its board subscription, carried to the new session
	sleeping     *store.LiveSession // set when it is asleep: it has no terminal
	fresh        *store.LiveSession // set when it starts in a new terminal (asleep, or its terminal is gone)
	pane         *ptymanager.PaneInfo
	workdir      string
	usePTY       bool
	resuming     bool // a Resume, which takes the same steps (#4809)

	newSessionID, newSessionName, newLogPath string

	// The settings it starts with: the stored ones, with the request's changes.
	flags                                                     []string
	prompt, board, boardServer, displayName, boardType, model string
	thinking                                                  string // the thinking level it starts with ("" = the CLI's default)
	thinkingFlag                                              bool   // the agent was started with a level, or this restart sets one: its stored --effort is replaced
	contextWindow                                             int
	caps                                                      *agent.Capabilities
	capsJSON, toolsJSON                                       *string
	tools                                                     []string
	permMode                                                  string
}

// Restart restarts the agent session: worked out first (restartResolve, restartSettings, restartCommand), so
// a restart that can't be done fails with the old agent still running; then the terminal, the board
// subscription and the start command, and the store.
// POST /api/sessions/live/{name}/restart
func (h *SessionsHandler) Restart(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	name := chi.URLParam(r, "name")
	var req restartRequest
	decodeJSON(r, &req)
	if namesNoSession(name, req.SessionID) {
		writeSessionIDRequired(w)
		return
	}
	end, free := h.beginRestart(name)
	if !free {
		refuseRestartBusy(w)
		return
	}
	defer end()
	ctx := viaHub(r.Context(), r)
	plan, fail := h.restartResolve(ctx, name, req)
	if fail != nil {
		fail(w)
		return
	}
	plan.restartSettings()
	cmd, fail := h.restartCommand(ctx, plan)
	if fail != nil {
		fail(w)
		return
	}
	ctx, cancel := lifecycleCtx(ctx) // from here the restart touches the terminals: it runs to its end whatever the request does
	defer cancel()
	target, fail := h.restartTerminal(ctx, plan)
	if fail != nil {
		fail(w)
		return
	}
	if fail := h.restartLaunch(ctx, plan, target, cmd); fail != nil {
		fail(w)
		return
	}
	h.restartRecord(ctx, plan)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "session_id": plan.newSessionID, "session_name": plan.newSessionName,
	})
}

// restartResolve works out the restart: the stored session (refused when it is no longer live or is archived),
// its type, its board subscription, the model it would start with (checked before anything is touched), and
// whether it starts in a new terminal or respawns its pane.
func (h *SessionsHandler) restartResolve(ctx context.Context, name string, req restartRequest) (*restartPlan, answer) {
	p := &restartPlan{req: req, agentType: req.AgentType}
	if req.SessionID != "" {
		if ls, err := h.ss.GetLiveSession(ctx, req.SessionID); err == nil && ls != nil {
			if ls.Status != "active" || ls.StoppedAt != nil {
				return nil, func(w http.ResponseWriter) {
					writeError(w, http.StatusConflict, "session_not_live", "session is not live")
				}
			}
			if ls.IsArchived == 1 {
				return nil, func(w http.ResponseWriter) {
					writeError(w, http.StatusConflict, "session_archived", "session is archived")
				}
			}
			p.stored = ls
			if ls.Capabilities != nil && *ls.Capabilities != "" {
				p.caps = &agent.Capabilities{}
				if err := json.Unmarshal([]byte(*ls.Capabilities), p.caps); err != nil {
					return nil, func(w http.ResponseWriter) {
						errInternalServer(w, fmt.Sprintf("corrupt stored capabilities: %v", err))
					}
				}
			}
			if ls.PermissionMode != nil && *ls.PermissionMode != "" {
				if !agent.ValidPermissionMode(*ls.PermissionMode) {
					return nil, func(w http.ResponseWriter) {
						errInternalServer(w, fmt.Sprintf("corrupt stored permission mode: %s", *ls.PermissionMode))
					}
				}
			}
			// Restart requests from older clients may omit agent_type. Keep the
			// persisted provider instead of silently switching Codex to Claude.
			if p.agentType == "" && ls.AgentType != "" {
				p.agentType = ls.AgentType
			}
		}
	}
	if p.agentType == "" {
		p.agentType = at.Claude
	}

	// Sessions joined through the board API may have no board_name in the
	// session store. Resolve their subscription before renaming the terminal.
	if h.bs != nil && req.SessionID != "" {
		oldAgentType := p.agentType
		if p.stored != nil {
			oldAgentType = p.stored.AgentType
		}
		sub, err := h.bs.GetSubscriptionBySessionName(ctx, naming.SessionName(oldAgentType, req.SessionID))
		if err != nil {
			return nil, func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
		}
		p.subscription = sub
	}

	// Validate the resolved model before touching the terminal or allocating a
	// replacement session. Restart carries the persisted model when the caller
	// omits one, so provider switches must reject stale cross-provider models
	// before any process or database state is mutated. When the caller explicitly
	// provides an empty model (""), they intend to clear the explicit override so
	// the agent falls back to its configured/default model.
	var preflightModel string
	if req.Model != nil {
		preflightModel = strings.TrimSpace(*req.Model)
	} else if p.stored != nil {
		preflightModel = strings.TrimSpace(ptr.Value(p.stored.Model))
	}
	if err := validateAgentModel(p.agentType, preflightModel); err != nil {
		return nil, func(w http.ResponseWriter) { errBadRequest(w, err.Error()) }
	}
	if req.Thinking != nil && !agentmodels.ValidThinking(p.agentType, strings.TrimSpace(*req.Thinking)) {
		msg := errInvalidThinking(p.agentType)
		return nil, func(w http.ResponseWriter) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg, "field": "thinking"})
		}
	}

	// A sleeping agent has no terminal. Restarting one starts a fresh agent in
	// a new terminal in its working directory, with the restart settings
	// (prompt, model, capabilities); waking would resume the old conversation.
	if p.stored != nil && p.stored.IsSleeping == 1 {
		p.sleeping = p.stored
	}
	// An awake agent whose own terminal is gone (it crashed or was closed) restarts the
	// same way: a fresh terminal in its working directory, never another agent's pane
	// (#3213; before, the restart answered terminal_gone until the agent showed asleep).
	p.fresh = p.sleeping
	if p.fresh == nil && p.stored != nil && p.stored.WorkingDir != "" {
		if pane, err := h.terminal.FindSession(ctx, name, p.agentType, req.SessionID); err == nil && pane == nil {
			p.fresh = p.stored
		}
	}

	p.newSessionID = h.uuid()
	p.newSessionName = naming.SessionName(p.agentType, p.newSessionID)
	p.newLogPath = naming.LogFile(h.cfg.LogDir, p.agentType, p.newSessionID)

	// Find the terminal before anything is touched.
	if p.fresh != nil {
		p.workdir = p.fresh.WorkingDir
		p.usePTY = h.restartsOnPTY(p.fresh)
		return p, nil
	}
	pane, err := h.terminal.FindSession(ctx, name, p.agentType, req.SessionID)
	if err == nil && pane == nil && req.SessionID != "" {
		return nil, func(w http.ResponseWriter) { writeTerminalError(w, ptymanager.ErrTerminalGone) }
	}
	if err != nil || pane == nil {
		return nil, func(w http.ResponseWriter) { errNotFound(w, "Pane not found") }
	}
	p.pane, p.workdir = pane, pane.CurrentPath
	// A live agent restarts on the backend it runs on: a pty one in a new pty terminal (pty has no rename, and a
	// tmux-style "<name>:0.0" target names no pty session, #4767).
	p.usePTY = h.restartsOnPTY(p.stored)
	return p, nil
}

// restartsOnPTY reports whether a restart runs the pty way: on a pty Coral every agent does (#3634); otherwise an
// agent the store records as on the pty backend, when there is one.
func (h *SessionsHandler) restartsOnPTY(ls *store.LiveSession) bool {
	if h.backend == nil {
		return false
	}
	if _, onPTY := h.backend.(*ptymanager.PTYBackend); onPTY {
		return true
	}
	return ls != nil && ls.Backend != nil && *ls.Backend == "pty"
}

// restartSettings is what the agent starts with: the stored session's settings (its board and title from its
// subscription), with the request's prompt, model and capabilities in their place when it sends them.
func (p *restartPlan) restartSettings() {
	var storedFlags []string
	if ls := p.stored; ls != nil {
		storedFlags = store.UnmarshalFlags(ls.Flags)
		p.prompt = ptr.Value(ls.Prompt)
		p.board = ptr.Value(ls.BoardName)
		p.boardServer = ptr.Value(ls.BoardServer)
		p.displayName = ptr.Value(ls.DisplayName)
		p.boardType = ptr.Value(ls.BoardType)
		p.model = ptr.Value(ls.Model)
		if t := ptr.Value(ls.Thinking); agentmodels.ValidThinking(p.agentType, t) { // another provider's level does not carry over
			p.thinking = t
		}
		p.thinkingFlag = ptr.Value(ls.Thinking) != ""
		p.contextWindow = ls.ContextWindow
		p.capsJSON = ls.Capabilities
		p.toolsJSON = ls.Tools
		p.tools = store.UnmarshalFlags(ls.Tools)
		// p.caps was validated and unmarshaled in restartResolve
	}
	if p.subscription != nil {
		p.board = p.subscription.Project
		if p.displayName == "" {
			p.displayName = p.subscription.JobTitle
		}
	}
	// Request body overrides stored values (user edited in modal)
	if p.req.Prompt != "" {
		p.prompt = p.req.Prompt
	}
	if p.req.Model != nil {
		if m := strings.TrimSpace(*p.req.Model); m != "" {
			p.model = m
			p.contextWindow = proxy.LookupContextWindow(m)
		} else {
			p.model = ""
			p.contextWindow = 0
		}
	}
	if p.req.Capabilities != nil {
		p.caps = p.req.Capabilities
		p.capsJSON = store.MarshalCapabilities(p.req.Capabilities)
	}
	if p.req.Thinking != nil {
		p.thinking = strings.TrimSpace(*p.req.Thinking) // checked in restartResolve
		p.thinkingFlag = true
	}
	if p.thinkingFlag {
		storedFlags = agentmodels.StripThinkingFlags(storedFlags)
	}
	if p.stored != nil && p.stored.PermissionMode != nil && *p.stored.PermissionMode != "" {
		p.permMode = *p.stored.PermissionMode
	} else {
		if perm := agent.PermissionModeFromFlags(storedFlags); perm != "" {
			p.permMode = perm
		}
	}
	cleanFlags := restartFlags(storedFlags, p.model, p.req.ExtraFlags)
	if p.caps.IsEmpty() && p.permMode != "" && p.permMode != "default" {
		cleanFlags = append(cleanFlags, agent.PermissionFlagsForAgent(p.agentType, p.permMode)...)
	}
	p.flags = append(cleanFlags, agentmodels.ThinkingArgs(p.agentType, p.thinking)...)
}

// restartFlags is a restarted agent's CLI flags: the stored ones without permission flags (its capabilities are
// the source of truth, and LaunchCommand generates the flags for the target type: a Codex --full-auto must not
// reach Claude) and without a model, then the model, then the request's extra flags.
func restartFlags(stored []string, model, extra string) []string {
	clean := stripAgentPermissionFlags(stored)
	var flags []string
	for i := 0; i < len(clean); i++ {
		flag := clean[i]
		if flag == "--model" || flag == "-m" {
			i++ // skip the value
			continue
		}
		if strings.HasPrefix(flag, "--model=") || strings.HasPrefix(flag, "-m=") {
			continue
		}
		flags = append(flags, flag)
	}
	if modelArgs, err := agentmodels.ModelArgs(model); err == nil { // a model that is not a valid id (a hand-edited store) is not put on the command line
		flags = append(flags, modelArgs...)
	}
	cleanExtra := stripAgentPermissionFlags(strings.Fields(extra))
	return append(flags, cleanExtra...)
}

// restartCommand builds the agent's start command, before anything is touched: when it can't be built (its prompt
// or settings files can't be written, #3601 W12), the restart fails with the old agent still running. A plain
// terminal has none: its restart is a fresh shell in the same folder, as a launch of one is (isTerminal there).
// Before, the restart typed a launch command for the Claude implementation that stands in for the type into the
// shell, which ran the claude on PATH, whatever cli_path_claude said (found by the CDP gate's PATH-CLI check on a
// restarted terminal pane).
func (h *SessionsHandler) restartCommand(ctx context.Context, p *restartPlan) (string, answer) {
	if p.agentType == at.Terminal {
		return "", nil
	}
	userSettings, serr := h.launchSettings(ctx)
	if serr != nil { // never start the restarted agent on the CLI found on PATH (#4350)
		return "", func(w http.ResponseWriter) {
			writeError(w, http.StatusServiceUnavailable, "", serr.Error())
		}
	}
	permMode := p.permMode
	if permMode == "" {
		permMode = setreg.Enum(userSettings, "default_permission_mode")
	}
	cliPath, cerr := agent.LaunchCLI(userSettings, p.agentType) // a broken cli_path_<agent> refuses the restart (#115)
	if cerr != nil {
		refuse, _ := cliPathRefusal(ctx, cerr)
		return "", refuse
	}
	cmd, err := launchCommand(agent.GetAgent(p.agentType), agent.LaunchParams{
		SessionID:       p.newSessionID,
		SessionName:     p.newSessionName,
		ProtocolPath:    h.protocolPath(),
		Flags:           p.flags,
		WorkingDir:      p.workdir,
		CLIPath:         cliPath, // Settings > CLI paths (#3205)
		BoardName:       p.board,
		BoardLabel:      h.boardLabel(p.board),
		Role:            naming.SubscriberID(p.displayName, p.agentType),
		Prompt:          p.prompt,
		PromptOverrides: promptOverrides(userSettings),
		BoardType:       p.boardType,
		Capabilities:    p.caps,
		Tools:           p.tools,
		PermissionMode:  permMode,
		CoralDir:        h.cfg.CoralDir(),
		CoralHost:       h.cfg.AgentHost(),
		CoralPort:       h.cfg.Port,
	})
	if err != nil {
		return "", func(w http.ResponseWriter) { errInternalServer(w, fmt.Sprintf("launch command failed: %v", err)) }
	}
	return cmd, nil
}

// restartTerminal gives the agent its terminal under the new session name: a fresh one (on pty also for a live
// agent, whose terminal is ended once the new one runs), or on tmux the old pane respawned and renamed, with its log started and its
// title set. It returns where the start command is typed.
func (h *SessionsHandler) restartTerminal(ctx context.Context, p *restartPlan) (string, answer) {
	fail := func(msg string) answer { return func(w http.ResponseWriter) { errInternalServer(w, msg) } }
	var target string
	if p.fresh != nil || p.usePTY {
		if p.usePTY {
			if err := h.backend.Spawn(p.newSessionName, p.agentType, p.workdir, p.newSessionID, "", 200, 50); err != nil {
				return "", fail(fmt.Sprintf("pty spawn failed: %v", err))
			}
			if p.pane != nil {
				// A live pty terminal: ended once its replacement runs under the new name (pty has no rename), so a
				// spawn that fails leaves the agent running. One whose process exited since it was found is ended.
				if err := h.endOldTerminal(p.pane.SessionName); err != nil && !errors.Is(err, ptymanager.ErrTerminalGone) {
					h.backend.Kill(p.newSessionName)
					return "", fail(fmt.Sprintf("pty %s failed: %v", p.verb(), err))
				}
			}
		} else {
			ptymanager.ResetAgentLog(p.newLogPath) // 0600, never through a link (#3601, W17)
			if err := h.terminal.CreateSession(ctx, p.newSessionName, p.workdir); err != nil {
				return "", fail(err.Error())
			}
			h.terminal.StartLogging(ctx, p.newSessionName, p.newLogPath)
		}
		target = p.newSessionName + ".0"
	} else {
		// Close old pipe-pane, respawn, rename
		h.terminal.StopLogging(ctx, p.pane.Target)
		if err := h.terminal.RestartPane(ctx, p.pane.Target, p.pane.CurrentPath); err != nil {
			return "", fail(err.Error())
		}
		if err := h.terminal.RenameSession(ctx, p.pane.SessionName, p.newSessionName); err != nil {
			return "", fail(err.Error())
		}
		target = fmt.Sprintf("%s:0.0", p.newSessionName)
		time.Sleep(paneRespawnSettle)
		// Clear scrollback, create log, setup pipe-pane
		h.terminal.ClearHistory(ctx, target)
		ptymanager.ResetAgentLog(p.newLogPath) // 0600, never through a link (#3601, W17)
		h.terminal.StartLogging(ctx, target, p.newLogPath)
	}
	// Set pane title using native tmux command (avoids shell echo)
	if !p.usePTY {
		h.terminal.SetPaneTitle(ctx, target, fmt.Sprintf("%s — %s", p.folderName(), p.agentType))
	}
	return target, nil
}

// verb is what the plan does, for its messages.
func (p *restartPlan) verb() string {
	if p.resuming {
		return "resume"
	}
	return "restart"
}

// paneRespawnSettle is how long a respawned pane is given before its history is cleared and its log started.
var paneRespawnSettle = 500 * time.Millisecond

// restartKillGrace is how long a restart or resume gives the old pty terminal to exit on SIGTERM before it is force-killed. The new terminal
// already runs, the conversation continues from the agent's transcript, and tmux's respawn is immediate: an agent that ignores SIGTERM held
// every pty restart for Kill's full 5 s (#4842). A var for the tests.
var restartKillGrace = time.Second

// endOldTerminal ends the old terminal of a restart or resume: a pty one with the short grace, any other backend as Kill does.
func (h *SessionsHandler) endOldTerminal(name string) error {
	if k, ok := h.backend.(interface {
		KillWithin(name string, grace time.Duration) error
	}); ok {
		return k.KillWithin(name, restartKillGrace)
	}
	return h.backend.Kill(name)
}

func (p *restartPlan) folderName() string { return filepath.Base(strings.TrimRight(p.workdir, "/")) }

// restartLaunch moves the board subscription to the new session, then types the start command. The new session
// holds the subscription before its process starts: its first request (a hook, a board call) carries the new
// token, and with no subscription under the new name it is refused as an agent that is not on the board
// (identity_mismatch). The identity harness saw a restarted agent's first post refused 39 ms after its launch
// command. A start command that can't be typed puts the subscription back.
func (h *SessionsHandler) restartLaunch(ctx context.Context, p *restartPlan, target, cmd string) answer {
	if sub := p.subscription; sub != nil {
		if _, err := h.bs.Subscribe(ctx, sub.Project, sub.SubscriberID, sub.JobTitle, p.newSessionName,
			sub.OriginServer, sub.ReceiveMode, sub.CanPeek == 1); err != nil {
			return func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
		}
		writeBoardStateFile(p.newSessionName, sub.Project, sub.JobTitle, h.cfg)
	}
	if cmd == "" {
		return nil
	}
	log.Printf("[launch] %s session=%s cmd=%s", p.verb(), target, cmd)
	where, what := target, "launch command failed"
	if p.usePTY {
		if pb, ok := h.backend.(*ptymanager.PTYBackend); ok && !pb.WaitReady(p.newSessionName, 5*time.Second) {
			log.Printf("[launch] pty shell not ready after 5s, sending anyway: %s", p.newSessionName)
		}
		// Route through the terminal abstraction so oversized commands use the
		// protected script handoff instead of hitting the shell's canonical
		// input limit while a fresh PTY is initializing.
		where, what = p.newSessionName, "pty launch command failed"
	}
	if err := sendLaunchCommand(ctx, h.terminal, where, cmd); err != nil {
		h.putSubscriptionBack(ctx, p.subscription)
		return func(w http.ResponseWriter) { errInternalServer(w, fmt.Sprintf("%s: %v", what, err)) }
	}
	return nil
}

// restartRecord replaces the old session with the new one in the store, carrying its settings, name, tags and
// board forward; a sleeping agent is awake now.
func (h *SessionsHandler) restartRecord(ctx context.Context, p *restartPlan) {
	// Capture shell PID for process-tree-based identity resolution
	var restartPID int
	if tmuxTerm, ok := h.terminal.(*ptymanager.TmuxSessionTerminal); ok {
		if pid, err := tmuxTerm.Client().GetPanePID(ctx, p.newSessionName); err == nil {
			restartPID = pid
			log.Printf("[launch] restart captured PID %d for session %s", pid, p.newSessionName)
		} else {
			log.Printf("[launch] restart failed to capture PID for session %s: %v", p.newSessionName, err)
		}
	}
	// An empty model is stored as "" (not NULL) and empty flags as "[]", so a cleared model or cleared flags stay
	// cleared on the next restart rather than carrying forward the prior ones.
	model, flags := ptr.NonEmpty(p.model), store.MarshalFlags(p.flags)
	if p.model == "" {
		model = new(string)
	}
	if len(p.flags) == 0 {
		empty := "[]"
		flags = &empty
	}
	h.chatMeta.forget(p.req.SessionID, p.newSessionID)
	h.ss.ReplaceLiveSession(ctx, p.req.SessionID, &store.LiveSession{
		SessionID:     p.newSessionID,
		AgentType:     p.agentType,
		AgentName:     p.folderName(),
		WorkingDir:    p.workdir,
		ResumeFromID:  ptr.NonEmpty(p.req.SessionID),
		Flags:         flags,
		Prompt:        ptr.NonEmpty(p.prompt),
		BoardName:     ptr.NonEmpty(p.board),
		BoardServer:   ptr.NonEmpty(p.boardServer),
		BoardType:     ptr.NonEmpty(p.boardType),
		Capabilities:  p.capsJSON,
		Model:         model,
		Thinking:      &p.thinking, // "" when it has none: the replaced session's level is not carried over
		ContextWindow: p.contextWindow,
		Tools:         p.toolsJSON,
		PID:           restartPID,
		PermissionMode: func() *string {
			if p.stored != nil {
				return p.stored.PermissionMode
			}
			return nil
		}(),
	})
	h.noteLaunchedHooks(ctx, p.newSessionID, p.agentType) // #3665
	h.ss.MigrateDisplayName(ctx, p.req.SessionID, p.newSessionID)
	h.ss.MigrateSessionTags(ctx, p.req.SessionID, p.newSessionID) // and its tags (#3216)
	if p.sleeping != nil {
		// The new row inherits the sleeping flag; this agent is running now.
		h.ss.SetSessionSleeping(ctx, p.newSessionID, false)
		if bn := ptr.Value(p.sleeping.BoardName); bn != "" && h.boardHandler != nil {
			h.boardHandler.SetPaused(bn, false)
		}
	}
	// Re-subscribed in restartLaunch, before the launch; a restart with no subscription joins its board now.
	if p.subscription == nil && p.board != "" {
		h.setupBoardAndPrompt(p.newSessionID, p.newSessionName, p.agentType, p.board, p.displayName)
	}
}

// Resume restarts with --resume to continue a historical session: worked out first (resumeResolve, resumeCommand),
// so a resume that can't be done fails with the old agent still running; then Restart's own steps for the terminal,
// the board subscription and the start command (#4809: the #4767 fix had to be made in both copies); then the store.
// POST /api/sessions/live/{name}/resume
func (h *SessionsHandler) Resume(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	name := chi.URLParam(r, "name")
	var body resumeRequest
	if err := decodeJSON(r, &body); err != nil || body.SessionID == "" {
		errBadRequest(w, "session_id is required")
		return
	}
	end, free := h.beginRestart(name)
	if !free {
		refuseRestartBusy(w)
		return
	}
	defer end()
	ctx := viaHub(r.Context(), r)
	p, fail := h.resumeResolve(ctx, name, &body)
	if fail != nil {
		fail(w)
		return
	}
	slog.Info("resuming agent", "agent_name", name, "session_id", body.SessionID, "agent_type", p.agentType)
	agentImpl := agent.GetAgent(p.agentType)
	agent.TryPrepareResume(agentImpl, body.SessionID, p.workdir)
	cmd, fail := h.resumeCommand(ctx, p, agentImpl, body.SessionID)
	if fail != nil {
		fail(w)
		return
	}
	ctx, cancel := lifecycleCtx(ctx) // as Restart: from here it runs to its end whatever the request does
	defer cancel()
	target, fail := h.restartTerminal(ctx, p)
	if fail != nil {
		fail(w)
		return
	}
	if fail := h.restartLaunch(ctx, p, target, cmd); fail != nil {
		fail(w)
		return
	}
	h.resumeRecord(ctx, p, body.SessionID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "session_id": p.newSessionID, "session_name": p.newSessionName,
	})
}

// resumeRequest is a Resume request: the conversation to resume, and the live session it replaces.
type resumeRequest struct {
	SessionID        string `json:"session_id"`
	AgentType        string `json:"agent_type"`
	CurrentSessionID string `json:"current_session_id"`
}

// resumeResolve works out the resume as a restart plan: the type (never a terminal, and one that can resume), the
// live session it replaces (refused when unknown, no longer live or archived), its terminal or, when that is gone,
// its working directory (never another agent's pane, #3213), its stored settings and its board subscription.
func (h *SessionsHandler) resumeResolve(ctx context.Context, name string, body *resumeRequest) (*restartPlan, answer) {
	agentType := body.AgentType
	if agentType == "" {
		agentType = at.Claude
	}
	if agentType == at.Terminal { // a plain shell has no conversation to resume (and no agent CLI to type)
		return nil, func(w http.ResponseWriter) { errBadRequest(w, "Resume not supported for terminal") }
	}
	if !agent.IsResumeID(agentType, body.SessionID) {
		return nil, func(w http.ResponseWriter) { errBadRequest(w, "session_id is not a valid resume id") } // #3582: it goes into file paths
	}
	if !agent.GetAgent(agentType).SupportsResume() {
		return nil, func(w http.ResponseWriter) { errBadRequest(w, fmt.Sprintf("Resume not supported for %s", agentType)) }
	}
	targetSID := body.CurrentSessionID
	if targetSID == "" {
		if _, sid := logparse.ParseSessionName(name); sid != "" {
			targetSID = sid
		} else if _, err := uuid.Parse(name); err == nil {
			targetSID = name
		}
	}
	if namesNoSession(name, targetSID) {
		return nil, writeSessionIDRequired
	}
	ls, err := h.ss.GetLiveSession(ctx, targetSID)
	switch {
	case err != nil:
		slog.Warn("failed to validate live session metadata", "session_id", targetSID, "error", err)
		return nil, func(w http.ResponseWriter) {
			writeError(w, http.StatusServiceUnavailable, "agent_metadata_unavailable", "Agent metadata is temporarily unavailable; try again.")
		}
	case ls == nil:
		return nil, func(w http.ResponseWriter) {
			writeError(w, http.StatusNotFound, "session_not_found", "live session not found")
		}
	case ls.Status != "active" || ls.StoppedAt != nil:
		return nil, func(w http.ResponseWriter) {
			writeError(w, http.StatusConflict, "session_not_live", "session is not live")
		}
	case ls.IsArchived == 1:
		return nil, func(w http.ResponseWriter) {
			writeError(w, http.StatusConflict, "session_archived", "session is archived")
		}
	}
	body.CurrentSessionID = targetSID

	p := &restartPlan{req: restartRequest{SessionID: targetSID, AgentType: agentType}, agentType: agentType, stored: ls, resuming: true}
	pane, _ := h.terminal.FindSession(ctx, name, agentType, targetSID)
	switch {
	case pane != nil:
		p.pane, p.workdir = pane, pane.CurrentPath
	case ls.WorkingDir != "":
		p.fresh, p.workdir = ls, ls.WorkingDir
	default:
		return nil, func(w http.ResponseWriter) { writeTerminalError(w, ptymanager.ErrTerminalGone) }
	}
	p.usePTY = h.restartsOnPTY(ls)
	p.newSessionID = h.uuid()
	p.newSessionName = naming.SessionName(agentType, p.newSessionID)
	p.newLogPath = naming.LogFile(h.cfg.LogDir, agentType, p.newSessionID)
	p.prompt, p.board, p.boardServer = ptr.Value(ls.Prompt), ptr.Value(ls.BoardName), ptr.Value(ls.BoardServer)
	p.displayName, p.boardType, p.tools = ptr.Value(ls.DisplayName), ptr.Value(ls.BoardType), store.UnmarshalFlags(ls.Tools)
	if ls.Capabilities != nil && *ls.Capabilities != "" {
		p.caps = &agent.Capabilities{}
		if err := json.Unmarshal([]byte(*ls.Capabilities), p.caps); err != nil {
			return nil, func(w http.ResponseWriter) {
				errInternalServer(w, fmt.Sprintf("corrupt stored capabilities: %v", err))
			}
		}
		p.capsJSON = ls.Capabilities
	}
	if ls.PermissionMode != nil && *ls.PermissionMode != "" {
		if !agent.ValidPermissionMode(*ls.PermissionMode) {
			return nil, func(w http.ResponseWriter) {
				errInternalServer(w, fmt.Sprintf("corrupt stored permission mode: %s", *ls.PermissionMode))
			}
		}
		p.permMode = *ls.PermissionMode
	} else {
		storedFlags := store.UnmarshalFlags(ls.Flags)
		p.permMode = agent.PermissionModeFromFlags(storedFlags)
	}
	if ls.Model != nil {
		p.model = *ls.Model
	}
	p.contextWindow = ls.ContextWindow
	storedFlags := store.UnmarshalFlags(ls.Flags)
	cleanFlags := stripAgentPermissionFlags(storedFlags)
	cleanFlags = agentmodels.StripThinkingFlags(cleanFlags)
	if p.caps.IsEmpty() && p.permMode != "" && p.permMode != "default" {
		cleanFlags = append(cleanFlags, agent.PermissionFlagsForAgent(agentType, p.permMode)...)
	}
	if t := ptr.Value(ls.Thinking); agentmodels.ValidThinking(agentType, t) { // the resumed agent keeps its level (a resume starts with no other stored flag)
		p.thinking = t
	}
	p.flags = append(cleanFlags, agentmodels.ThinkingArgs(agentType, p.thinking)...)
	// The agent's board subscription (job title, receive mode, peek), carried to the new session as restart does
	// (#3216); a resume that can't read it goes on without (it then joins its board after the launch).
	if h.bs != nil {
		storedAgentType := agentType
		if ls.AgentType != "" {
			storedAgentType = ls.AgentType
		}
		p.subscription, _ = h.bs.GetSubscriptionBySessionName(ctx, naming.SessionName(storedAgentType, targetSID))
	}
	return p, nil
}

// resumeCommand builds the resume's start command, before the terminal is touched: when it can't be built
// (#3601 W12), the resume fails with the old agent still running.
func (h *SessionsHandler) resumeCommand(ctx context.Context, p *restartPlan, agentImpl agent.Agent, resumeID string) (string, answer) {
	userSettings, serr := h.launchSettings(ctx)
	if serr != nil { // never resume the agent on the CLI found on PATH (#4350)
		return "", func(w http.ResponseWriter) {
			writeError(w, http.StatusServiceUnavailable, "", serr.Error())
		}
	}
	permMode := p.permMode
	if permMode == "" {
		permMode = setreg.Enum(userSettings, "default_permission_mode")
	}
	cliPath, cerr := agent.LaunchCLI(userSettings, p.agentType) // a broken cli_path_<agent> refuses the resume (#115)
	if cerr != nil {
		refuse, _ := cliPathRefusal(ctx, cerr)
		return "", refuse
	}
	cmd, err := launchCommand(agentImpl, agent.LaunchParams{
		SessionID:       p.newSessionID,
		SessionName:     p.newSessionName,
		ProtocolPath:    h.protocolPath(),
		ResumeSessionID: resumeID,
		Flags:           p.flags,
		WorkingDir:      p.workdir,
		CLIPath:         cliPath, // Settings > CLI paths (#3205)
		BoardName:       p.board,
		BoardLabel:      h.boardLabel(p.board),
		Role:            naming.SubscriberID(p.displayName, p.agentType),
		Prompt:          p.prompt,
		PromptOverrides: promptOverrides(userSettings),
		BoardType:       p.boardType,
		Capabilities:    p.caps,
		Tools:           p.tools,
		PermissionMode:  permMode,
		CoralDir:        h.cfg.CoralDir(),
		CoralHost:       h.cfg.AgentHost(),
		CoralPort:       h.cfg.Port,
	})
	if err != nil {
		return "", func(w http.ResponseWriter) { errInternalServer(w, err.Error()) }
	}
	return cmd, nil
}

// resumeRecord replaces the old session with the resumed one in the store: its working directory, the conversation
// it resumes, its prompt and board; its name and tags carried forward, as a restart's (#3216); awake (#3273).
func (h *SessionsHandler) resumeRecord(ctx context.Context, p *restartPlan, resumeID string) {
	var resumePID int
	if tmuxTerm, ok := h.terminal.(*ptymanager.TmuxSessionTerminal); ok {
		if pid, err := tmuxTerm.Client().GetPanePID(ctx, p.newSessionName); err == nil {
			resumePID = pid
		}
	}
	oldID := p.req.SessionID
	h.chatMeta.forget(oldID, p.newSessionID)
	h.ss.ReplaceLiveSession(ctx, oldID, &store.LiveSession{
		SessionID:     p.newSessionID,
		AgentType:     p.agentType,
		AgentName:     p.folderName(),
		WorkingDir:    p.workdir,
		ResumeFromID:  ptr.NonEmpty(resumeID),
		Prompt:        ptr.NonEmpty(p.prompt),
		BoardName:     ptr.NonEmpty(p.board),
		BoardServer:   ptr.NonEmpty(p.boardServer),
		BoardType:     ptr.NonEmpty(p.boardType),
		Thinking:      &p.thinking,
		PID:           resumePID,
		Flags:         store.MarshalFlags(p.flags),
		Capabilities:  p.capsJSON,
		Model:         ptr.NonEmpty(p.model),
		ContextWindow: p.contextWindow,
		Tools:         store.MarshalCapabilities(p.tools),
		PermissionMode: func() *string {
			if p.stored != nil {
				return p.stored.PermissionMode
			}
			return nil
		}(),
	})
	h.noteLaunchedHooks(ctx, p.newSessionID, p.agentType) // #3665
	h.ss.MigrateDisplayName(ctx, oldID, p.newSessionID)
	h.ss.MigrateSessionTags(ctx, oldID, p.newSessionID)
	// The new row inherits the sleeping flag; the resumed agent is running now, as after a restart. Left asleep,
	// team wake and the board notifier skipped it until the server restarted (#3273).
	h.ss.SetSessionSleeping(ctx, p.newSessionID, false)
	if p.stored.IsSleeping == 1 && p.board != "" && h.boardHandler != nil {
		h.boardHandler.SetPaused(p.board, false)
	}
	// Re-subscribed in restartLaunch, before the launch (subscriber_id, the role, is stable, so the cursor
	// persists); a resume with no subscription joins its board now.
	if p.subscription == nil && p.board != "" {
		h.setupBoardAndPrompt(p.newSessionID, p.newSessionName, p.agentType, p.board, p.displayName)
	}
}

// Attach opens a native terminal attached to the tmux session.
// POST /api/sessions/live/{name}/attach
func (h *SessionsHandler) Attach(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		AgentType string `json:"agent_type"`
		SessionID string `json:"session_id"`
	}
	decodeJSON(r, &body)
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
		defer safego.Recover("server/routes/sessions_lifecycle:840")
		_ = executil.Run(osascriptTimeout, "osascript", terminalAttachArgs(attachCmd)...)
	}()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// osascriptTimeout bounds the Terminal attach: a macOS automation-permission dialog that nobody
// answers must not hold the process and its goroutine for good (#3999).
var osascriptTimeout = 30 * time.Second

// Pass the shell command as data, so quotes and backslashes in app paths never
// become AppleScript source.
func terminalAttachArgs(command string) []string {
	return []string{"-e", `on run argv
    tell application "Terminal" to do script (item 1 of argv)
end run`, "--", command}
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
			defer safego.Recover("server/routes/sessions_lifecycle:901")
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

		result, err := h.launchSession(ctx, launchSpec{
			WorkDir: workingDir, AgentType: agentType, DisplayName: m.AgentName, Prompt: resurrectPrompt, BoardName: team.Name,
			Model: agentCfg.Model, Capabilities: agentCfg.Capabilities, Tools: agentCfg.Tools, Hooks: agentCfg.Hooks,
		})
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
