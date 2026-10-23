package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentmodels"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/ptymanager"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
)

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
	ctx, cancel := lifecycleCtx(r.Context()) // a team wake runs to its end whatever the request does (#5171)
	defer func() { cancel() }()

	// Find sleeping sessions on this board and relaunch
	allLive, _ := h.ss.GetAllLiveSessions(ctx)
	sleepingHere := 0
	for _, ls := range allLive {
		if ls.IsSleeping == 1 && ls.IsArchived != 1 && ls.BoardName != nil && *ls.BoardName == boardName {
			sleepingHere++
		}
	}
	cancel()
	ctx, cancel = lifecycleCtxN(r.Context(), sleepingHere) // the bound grows with the team (#5213)

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

// codexSessionSaved is agent.CodexSessionSaved (a variable so a test can say).
var codexSessionSaved = agent.CodexSessionSaved

// wakeExistingSession recreates the tmux/pty session for a sleeping agent
// using the EXISTING session ID. Preserves display name, board subscriptions, and history.
// wakeFlags is the agent's stored flags with the model and thinking level it runs now: a live switch (#4969) changes the stored model and level, not the flags the agent was started with, so a wake that
// used the stored flags as they were started the agent again at the old level (#6219). The model's and the level's own flags are replaced, never doubled.
func wakeFlags(ls *store.LiveSession) []string {
	flags := store.UnmarshalFlags(ls.Flags)
	if m := strings.TrimSpace(ptr.Value(ls.Model)); m != "" {
		if modelArgs, err := agentmodels.ModelArgs(m); err == nil {
			var kept []string
			for i := 0; i < len(flags); i++ {
				f := flags[i]
				if f == "--model" || f == "-m" {
					i++ // its value
					continue
				}
				if strings.HasPrefix(f, "--model=") || strings.HasPrefix(f, "-m=") {
					continue
				}
				kept = append(kept, f)
			}
			flags = append(kept, modelArgs...)
		}
	}
	if level := ptr.Value(ls.Thinking); level != "" && agentmodels.ValidThinking(ls.AgentType, level) {
		flags = append(agentmodels.StripThinkingFlags(flags), agentmodels.ThinkingArgs(ls.AgentType, level)...)
	}
	return flags
}

func (h *SessionsHandler) wakeExistingSession(ctx context.Context, ls *store.LiveSession) (err error) {
	launched := false // the agent CLI was started again: it loaded the current hook set
	defer func() {
		if err == nil && launched {
			h.noteLaunchedHooks(ctx, ls.SessionID, ls.AgentType)
			_ = h.ss.StampSessionSettings(ctx, ls.SessionID) // the CLI now runs with the stored model and level: turns the transcript recorded before are older than them (#6225)
		}
	}()
	flags := wakeFlags(ls)
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

	userSettings, serr := h.launchSettings(ctx)
	if serr != nil { // never wake the agent on the CLI found on PATH (#4350)
		return serr
	}
	cliPath, err := agent.LaunchCLI(userSettings, ls.AgentType) // a broken cli_path_<agent> refuses the wake (#115)
	if err != nil {
		return err
	}

	role := displayName
	if role == "" {
		role = ls.AgentType
	}

	var caps *agent.Capabilities
	if ls.Capabilities != nil && *ls.Capabilities != "" {
		caps = &agent.Capabilities{}
		if err := json.Unmarshal([]byte(*ls.Capabilities), caps); err != nil {
			return fmt.Errorf("corrupt stored capabilities: %w", err)
		}
	}
	var permMode string
	if ls.PermissionMode != nil && *ls.PermissionMode != "" {
		if !agent.ValidPermissionMode(*ls.PermissionMode) {
			return fmt.Errorf("corrupt stored permission mode: %s", *ls.PermissionMode)
		}
		permMode = *ls.PermissionMode
	} else {
		permMode = agent.PermissionModeFromFlags(flags)
		if permMode == "" {
			permMode = setreg.Enum(userSettings, "default_permission_mode")
		}
	}
	if caps.IsEmpty() && permMode != "" && permMode != "default" {
		if agent.PermissionModeFromFlags(flags) == "" {
			flags = append(flags, agent.PermissionFlagsForAgent(ls.AgentType, permMode)...)
		}
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
		Capabilities:    caps,
		Tools:           store.UnmarshalFlags(ls.Tools),
		CLIPath:         cliPath,
		PermissionMode:  permMode,
		CoralDir:        h.cfg.CoralDir(),
		CoralHost:       h.cfg.AgentHost(),
		CoralPort:       h.cfg.Port,
		RunTag:          h.runTagFor(ctx, ls.SessionID, "", ""),
	}

	// A Codex agent that never completed a turn has no saved session: "codex resume" would end at "No saved session found" and a bare shell. It starts a new one
	// (a wake is asked to bring the agent back, not to resume a particular conversation) (#5222).
	if ls.AgentType == at.Codex && !codexSessionSaved(ls.SessionID) {
		log.Printf("[wake] session=%s: Codex has no saved session for it (it never completed a turn); starting a new one", sessionName)
		launchParams.ResumeSessionID = ""
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
				return spawnError(err)
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

	h.modelSwitches.end(sessionID, "The agent was put to sleep before the change was confirmed.") // #6219
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
	ctx, cancel := lifecycleCtx(r.Context()) // a wake runs to its end whatever the request does (#5171)
	defer cancel()

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
		if refuse, ok := cliPathRefusal(viaHub(ctx, r), err); ok { // the setting to fix, not a bare "failed" (#115)
			refuse(w)
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
	defer h.kickWS()                         // the feed shows the change now, not on the next tick
	ctx, cancel := lifecycleCtx(r.Context()) // a wake of all runs to its end whatever the request does (#5171)
	defer func() { cancel() }()
	allLive, _ := h.ss.GetAllLiveSessions(ctx)

	var sleeping []store.LiveSession
	for _, ls := range allLive {
		if ls.IsSleeping == 1 && ls.IsArchived == 0 { // archived agents are left asleep
			sleeping = append(sleeping, ls)
		}
	}
	cancel()
	ctx, cancel = lifecycleCtxN(r.Context(), len(sleeping)) // 2 minutes and 5 s a session: the last of many is not cut off (#5213)
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

// sleepReason is why Coral put a sleeping agent to sleep (#3344): "terminal_gone" when its
// terminal session no longer exists, "" when the user (or a team action) did.
func sleepReason(ls store.LiveSession) string {
	if ls.SleepReason == nil {
		return ""
	}
	return *ls.SleepReason
}
