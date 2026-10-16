package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/logparse"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/safego"
	"github.com/cdknorow/coral/internal/store"
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

func sendLaunchCommand(ctx context.Context, terminal ptymanager.SessionTerminal, target, command string) error {
	if err := terminal.SendToTarget(ctx, target, command); err != nil {
		return fmt.Errorf("launch command delivery to %s failed: %w", target, err)
	}
	return nil
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
		preflightModel = strings.TrimSpace(ptr.Value(storedSession.Model))
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
			storedPrompt = ptr.Value(ls.Prompt)
			storedBoard = ptr.Value(ls.BoardName)
			storedBoardServer = ptr.Value(ls.BoardServer)
			storedDisplayName = ptr.Value(ls.DisplayName)
			storedBoardType = ptr.Value(ls.BoardType)
			storedModel = ptr.Value(ls.Model)
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
		storedModelPtr = ptr.NonEmpty(storedModel)
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
		ResumeFromID:  ptr.NonEmpty(body.SessionID),
		Flags:         storedFlagsJSON,
		Prompt:        ptr.NonEmpty(storedPrompt),
		BoardName:     ptr.NonEmpty(storedBoard),
		BoardServer:   ptr.NonEmpty(storedBoardServer),
		BoardType:     ptr.NonEmpty(storedBoardType),
		Capabilities:  storedCapsJSON,
		Model:         storedModelPtr,
		ContextWindow: storedContextWindow,
		Tools:         storedToolsJSON,
		PID:           restartPID,
	})
	h.noteLaunchedHooks(ctx, newSessionID, agentType) // #3665
	h.ss.MigrateDisplayName(ctx, body.SessionID, newSessionID)
	h.ss.MigrateSessionTags(ctx, body.SessionID, newSessionID) // and its tags (#3216)
	if sleeping != nil {
		// The new row inherits the sleeping flag; this agent is running now.
		h.ss.SetSessionSleeping(ctx, newSessionID, false)
		if bn := ptr.Value(sleeping.BoardName); bn != "" && h.boardHandler != nil {
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
	storedPrompt := ptr.Value(ls.Prompt)
	storedSleeping := ls.IsSleeping == 1
	storedAgentType := agentType
	if ls.AgentType != "" {
		storedAgentType = ls.AgentType
	}
	storedBoard := ptr.Value(ls.BoardName)
	storedBoardServer := ptr.Value(ls.BoardServer)
	storedDisplayName := ptr.Value(ls.DisplayName)
	storedBoardType := ptr.Value(ls.BoardType)
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
		ResumeFromID: ptr.NonEmpty(body.SessionID),
		Prompt:       ptr.NonEmpty(storedPrompt),
		BoardName:    ptr.NonEmpty(storedBoard),
		BoardServer:  ptr.NonEmpty(storedBoardServer),
		BoardType:    ptr.NonEmpty(storedBoardType),
		PID:          resumePID,
	})
	h.noteLaunchedHooks(ctx, newSessionID, agentType) // #3665

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
		defer safego.Recover("server/routes/sessions_lifecycle:840")
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
