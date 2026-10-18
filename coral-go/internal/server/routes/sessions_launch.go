package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cdknorow/coral/internal/safego"
	"github.com/cdknorow/coral/internal/testguard"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/agentmodels"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/safefs"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
	"github.com/cdknorow/coral/internal/tracking"
)

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

	// An agent never starts a less-restricted agent (#4032).
	if agentCaller(r) {
		body.Flags, body.Capabilities, body.Tools, body.Hooks = limitAgentLaunch("launch", body.Flags, body.Capabilities, body.Tools, body.Hooks)
	}

	// Resolve model: request wins, else per-type default from settings.
	effectiveModel := h.resolveModel(r.Context(), body.AgentType, body.Model)
	if effectiveModel != "" && !agentmodels.ValidModelValue(effectiveModel) {
		attempt.Finish(tracking.OutcomeFailure, tracking.FailInvalidRequest, 0, 1)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errInvalidModel, "field": "model"})
		return
	}

	// Add model flag if resolved to non-empty
	launchFlags := body.Flags
	if effectiveModel != "" {
		launchFlags = append(append([]string{}, body.Flags...), "--model", effectiveModel)
	}
	result, err := h.launchSession(r.Context(), launchSpec{
		WorkDir: body.WorkingDir, AgentType: body.AgentType, DisplayName: body.DisplayName, ResumeSessionID: body.ResumeSessionID,
		Flags: launchFlags, Prompt: body.Prompt, BoardName: body.BoardName, BoardServer: body.BoardServer, Backend: body.Backend,
		BoardType: body.BoardType, Model: effectiveModel, Capabilities: body.Capabilities, Tools: body.Tools, Hooks: body.Hooks,
	})
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

// errInvalidModel: a model goes to the CLI as --model's one argument, so it must be a model id
// (#4065, Security's condition 3).
const errInvalidModel = "model must be a model id: letters, digits and . _ : / @ [ ] + -, at most 128, not starting with -"

// launchTeamAgent is one member of a team launch.
type launchTeamAgent struct {
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
}

// launchTeamRequest is a team launch's body.
type launchTeamRequest struct {
	BoardName          string            `json:"board_name"`
	BoardID            string            `json:"board_id"`  // relaunch on this board (#3536)
	NewBoard           bool              `json:"new_board"` // create another board even if the name exists
	WorkingDir         string            `json:"working_dir"`
	AgentType          string            `json:"agent_type"`
	Flags              []string          `json:"flags"`
	BoardServer        string            `json:"board_server"`
	Backend            string            `json:"backend"`
	BoardType          string            `json:"board_type"`
	Worktree           bool              `json:"worktree"`
	BaseBranch         string            `json:"base_branch"`
	Agents             []launchTeamAgent `json:"agents"`
	TeamKnowledgeIndex string            `json:"team_knowledge_index"`
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

// LaunchTeam launches multiple agents on a shared message board: the board decided and the request checked
// before anything is written, then the template's knowledge, working mode and presets restored, the worktree
// and the team record made, and each named member launched (one member's failure is its entry's error).
// POST /api/sessions/launch-team
func (h *SessionsHandler) LaunchTeam(w http.ResponseWriter, r *http.Request) {
	defer h.kickWS() // the feed shows the change now, not on the next tick
	var body launchTeamRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// A team is a handful of agents; a launch-team that names hundreds would start that many CLI processes (an agent token may call
	// this, #4602). The larger ceiling on live sessions an agent token started is a separate setting.
	if len(body.Agents) > maxLaunchTeamAgents {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("a team can have at most %d agents per launch", maxLaunchTeamAgents), "code": "too_many_agents", "max": maxLaunchTeamAgents})
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
	h.restoreTeamTemplate(r.Context(), &body)

	memberTypes, skippedMembers := teamMemberTypes(body)
	attempt := tracking.StartLaunch(tracking.KindTeam, tracking.TeamProvider(memberTypes), h.launchBackendLabel(body.Backend), len(body.Agents), false)
	refuse := func(category, msg string) {
		attempt.Finish(tracking.OutcomeFailure, category, 0, len(body.Agents))
		errBadRequest(w, msg)
	}
	if msg := checkTeamRequest(body); msg != "" {
		refuse(tracking.FailInvalidRequest, msg)
		return
	}
	ctx := r.Context()
	workingDir, worktreePath, msg := h.createTeamWorktree(ctx, body)
	if msg != "" {
		refuse(tracking.FailWorktreeFailed, msg)
		return
	}
	teamID := h.createTeamRecord(ctx, body, workingDir, worktreePath)

	// Load settings once so we can fall back to per-type default_model_<type>. A read that fails
	// stops the team before any agent starts: the loop below would otherwise start its agents
	// without the configured CLI path (#4350).
	userSettings, err := h.launchSettings(ctx)
	if err != nil {
		attempt.Finish(tracking.OutcomeFailure, tracking.FailSpawnFailed, 0, len(body.Agents))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	// An agent never starts a less-restricted agent (#4032): the team's own flags and each
	// member's choices are limited, except a member that is exactly an entry of a team template
	// the Operator saved (saved_team_templates, which an agent's token can't write).
	fromAgent := agentCaller(r)
	if fromAgent {
		body.Flags = keepAgentLaunchFlags(body.Flags)
	}
	var launched []map[string]any
	firstFailure := ""
	for _, agentDef := range body.Agents {
		if agentDef.Name == "" {
			continue
		}
		if fromAgent && !inSavedTeamTemplate(userSettings, agentDef) {
			agentDef.Flags, agentDef.Capabilities, agentDef.Tools, agentDef.Hooks = limitAgentLaunch("launch-team", agentDef.Flags, agentDef.Capabilities, agentDef.Tools, agentDef.Hooks)
		}
		entry, category := h.launchTeamMember(ctx, body, agentDef, userSettings, workingDir, worktreePath, teamID)
		if category != "" && firstFailure == "" {
			firstFailure = category
		}
		launched = append(launched, entry)
	}

	resp := map[string]any{"ok": true, "board": body.BoardName, "board_id": body.BoardName, "board_label": h.boardLabel(body.BoardName), "agents": launched}
	if teamID > 0 {
		resp["team_id"] = teamID
	}
	finishTeamLaunch(attempt, launched, skippedMembers, firstFailure, len(body.Agents))
	writeJSON(w, http.StatusOK, resp)
}

// restoreTeamTemplate writes what a team template brings besides its members: the members' knowledge files and the
// team's knowledge index, its working mode and its workflow presets. A failure is logged; the launch goes on.
func (h *SessionsHandler) restoreTeamTemplate(ctx context.Context, body *launchTeamRequest) {
	if body.BoardName != "" {
		coralDir := h.cfg.CoralDir()
		hasKnowledge := false
		for _, a := range body.Agents {
			if a.Knowledge != "" {
				if _, err := background.WriteKnowledgeFile(coralDir, body.BoardName, a.Name, a.Knowledge); err != nil {
					log.Printf("[launch-team] knowledge for agent %q of team %s not restored: %v", a.Name, body.BoardName, err)
				}
				hasKnowledge = true
			}
		}
		if body.TeamKnowledgeIndex != "" {
			if err := background.WriteKnowledgeIndex(coralDir, body.BoardName, body.TeamKnowledgeIndex); err != nil {
				log.Printf("[launch-team] knowledge index of team %s not restored: %v", body.BoardName, err)
			}
			hasKnowledge = true
		}
		if hasKnowledge {
			log.Printf("[launch-team] restored %d agent knowledge files for team %s", len(body.Agents), body.BoardName)
		}
	}
	if body.WorkingMode != nil && h.bs != nil {
		wm := board.WorkingMode{
			Mode:               body.WorkingMode.Mode,
			DependencyGuidance: body.WorkingMode.DependencyGuidance,
			CustomInstructions: body.WorkingMode.CustomInstructions,
		}
		if _, err := h.bs.SetWorkingMode(ctx, body.BoardName, wm); err != nil {
			log.Printf("[launch-team] failed to restore working mode for team %s: %v", body.BoardName, err)
		} else {
			log.Printf("[launch-team] restored working mode %q for team %s", wm.Mode, body.BoardName)
		}
	}
	if len(body.WorkflowPresets) > 0 && h.bs != nil {
		for _, p := range body.WorkflowPresets {
			h.bs.SaveWorkflowPreset(ctx, body.BoardName, p.ID, p.Name, p.Instructions, !p.Builtin)
		}
		log.Printf("[launch-team] restored %d workflow presets for team %s", len(body.WorkflowPresets), body.BoardName)
	}
}

// teamMemberTypes is the effective provider of each member that will launch (its own type, else the team's; the
// Claude fallback is launchSession's), for the team's analytics label ("mixed" only when those truly differ), and
// how many unnamed members the launch skips.
func teamMemberTypes(body launchTeamRequest) (types []string, skipped int) {
	for _, a := range body.Agents {
		if a.Name == "" {
			skipped++ // the launch loop skips unnamed members
			continue
		}
		t := body.AgentType
		if a.AgentType != "" {
			t = a.AgentType
		}
		types = append(types, t)
	}
	return types, skipped
}

// checkTeamRequest is why a team launch is refused before anything starts ("" when it isn't): the board, the
// folder and the members required, and the team's type and every member's type and model valid. A team that
// failed halfway would leave live agents behind.
func checkTeamRequest(body launchTeamRequest) string {
	if body.BoardName == "" || body.WorkingDir == "" || len(body.Agents) == 0 {
		return "board_name, working_dir, and agents required"
	}
	if err := agent.ValidateAgentType(body.AgentType); err != nil {
		return err.Error()
	}
	for _, a := range body.Agents {
		if err := agent.ValidateAgentType(a.AgentType); err != nil {
			return fmt.Sprintf("agent %q: %s", a.Name, err)
		}
		if a.Model != "" && !agentmodels.ValidModelValue(a.Model) {
			return fmt.Sprintf("agent %q: %s", a.Name, errInvalidModel)
		}
	}
	return ""
}

// createTeamWorktree makes the team's git worktree when it asks for one (branch coral-team/<board> from its base
// branch, main by default; the branch reused when a previous run left it), and returns the folder its agents start
// in, the worktree ("" for none), or why it failed.
func (h *SessionsHandler) createTeamWorktree(ctx context.Context, body launchTeamRequest) (workingDir, worktreePath, failure string) {
	if !body.Worktree || body.WorkingDir == "" {
		return body.WorkingDir, "", ""
	}
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
			return "", "", fmt.Sprintf("git worktree add failed: %s / %s", string(out), string(out2))
		}
	}
	log.Printf("[launch-team] created worktree at %s (branch %s from %s)", worktreePath, worktreeBranch, branch)
	return worktreePath, worktreePath, ""
}

// createTeamRecord stores the team (its request, its folder, whether that is a worktree) and returns its id, 0
// when there is no team store or the record could not be made.
func (h *SessionsHandler) createTeamRecord(ctx context.Context, body launchTeamRequest, workingDir, worktreePath string) int64 {
	if h.teamStore == nil {
		return 0
	}
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
		return 0
	}
	return team.ID
}

// launchTeamMember launches one named member: its type and model over the team's (the model else the Settings
// default for its type), its own flags or the team's (without their permission flags: launchSession derives those),
// the team's distilled knowledge in its prompt. It returns the member's entry for the reply ({name, error} when it
// failed) and, for a launch that failed, its analytics category.
func (h *SessionsHandler) launchTeamMember(ctx context.Context, body launchTeamRequest, def launchTeamAgent, userSettings map[string]string, workingDir, worktreePath string, teamID int64) (map[string]any, string) {
	agentType := body.AgentType
	if def.AgentType != "" {
		agentType = def.AgentType
	}
	effectiveModel := defaultModelFromSettings(userSettings, agentType, def.Model)
	agentFlags := def.Flags
	if len(agentFlags) == 0 {
		agentFlags = stripAgentPermissionFlags(body.Flags)
	}
	if effectiveModel != "" && !agentmodels.ValidModelValue(effectiveModel) {
		// Only a saved default can get here (each member's own model was checked above).
		return map[string]any{"name": def.Name, "error": "the default model in Settings: " + errInvalidModel}, ""
	}
	if effectiveModel != "" {
		agentFlags = append(agentFlags, "--model", effectiveModel)
	}
	result, err := h.launchSession(ctx, launchSpec{
		WorkDir: workingDir, AgentType: agentType, DisplayName: def.Name, Flags: agentFlags,
		Prompt:    InjectKnowledgeIntoPrompt(h.cfg.CoralDir(), body.BoardName, def.Name, def.Prompt),
		BoardName: body.BoardName, BoardServer: body.BoardServer, Backend: body.Backend, BoardType: body.BoardType,
		Model: effectiveModel, Capabilities: def.Capabilities, Tools: def.Tools, Hooks: def.Hooks,
	})
	if err != nil {
		log.Printf("[launch-team] failed to launch agent %s: %v", def.Name, err)
		return map[string]any{"name": def.Name, "error": err.Error()}, classifyLaunchError(err)
	}
	sessionID := result["session_id"].(string)
	if worktreePath != "" {
		h.ss.UpdateWorktreeInfo(ctx, sessionID, worktreePath, body.WorkingDir)
	}
	if teamID > 0 && h.teamStore != nil {
		agentConfigJSON, _ := json.Marshal(def)
		h.teamStore.CreateTeamMember(ctx, &store.TeamMember{
			TeamID:          teamID,
			AgentName:       def.Name,
			AgentConfigJSON: string(agentConfigJSON),
			SessionID:       &sessionID,
			Status:          "active",
		})
		h.ss.SetTeamID(ctx, sessionID, teamID)
	}
	// Board subscription handled by setupBoardAndPrompt (prompt passed as CLI arg)
	if body.BoardName != "" {
		h.setupBoardAndPrompt(sessionID, result["session_name"].(string), agentType, body.BoardName, launchedName(result, def.Name))
	}
	entry := map[string]any{"name": launchedName(result, def.Name), "session_id": sessionID, "session_name": result["session_name"]}
	if worktreePath != "" {
		entry["worktree_path"] = worktreePath
	}
	return entry, ""
}

// finishTeamLaunch records the team launch's outcome. launched holds both started members and {name, error}
// failures, so each kind is counted separately: only started members are real launches. Unnamed members were
// never launched: they count as failed (not started), with a fixed category, so requested == started + failed.
func finishTeamLaunch(attempt *tracking.Attempt, launched []map[string]any, skipped int, firstFailure string, requested int) {
	started, failed := 0, 0
	for _, m := range launched {
		if _, bad := m["error"]; bad {
			failed++
		} else {
			started++
		}
	}
	failed += skipped
	if skipped > 0 && firstFailure == "" {
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
			"agent_count": strconv.Itoa(started), "requested_agents": strconv.Itoa(requested),
			"started_agents": strconv.Itoa(started), "failed_agents": strconv.Itoa(failed),
		}
		tracking.TrackEvent(tracking.EventTeamLaunched, counts)
		tracking.TrackOnce(tracking.EventFirstTeamLaunched, map[string]string{"agent_count": strconv.Itoa(started)})
	}
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
		result, err := h.launchSession(bgCtx, launchSpec{
			WorkDir: cfg.WorkingDir, AgentType: cfg.AgentType, DisplayName: displayName, Flags: flags, Prompt: prompt,
			BoardName: boardName, BoardServer: boardServer, BoardType: boardType, Model: modelStr, Capabilities: caps, Tools: tools,
		})
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

// errLaunchSettings is what a launch answers when Coral's settings could not be read.
var errLaunchSettings = errors.New("Coral's settings could not be read, so the agent was not started (it would have run the CLI found on PATH instead of the one set in Settings > CLI Paths); try again")

// launchSettings reads the settings a launch builds its command from: the CLI path, the default
// model and permission mode. It reads with a context a client's hang-up cannot cancel (a team launch
// loops over its agents inside one request: when the request ended partway, every later agent read
// no settings and started the CLI found on PATH, the real one, instead of the configured one), and
// a read that fails fails the launch: it never falls back to PATH (#4350).
func (h *SessionsHandler) launchSettings(ctx context.Context) (map[string]string, error) {
	s, err := h.ss.GetSettingsForCLI(ctx)
	if err != nil || s == nil {
		slog.Error("reading settings for a launch failed", "error", err)
		return nil, errLaunchSettings
	}
	return s, nil
}

// launchSpec is one agent to launch.
type launchSpec struct {
	WorkDir, AgentType, DisplayName string
	ResumeSessionID                 string // a conversation to continue ("" for a new one)
	Flags                           []string
	Prompt                          string
	BoardName, BoardServer          string
	Backend                         string // the launch path asked for: "pty", "tmux" or "" (launchPath)
	BoardType, Model                string
	Capabilities                    *agent.Capabilities
	Tools                           []string
	Hooks                           map[string]interface{}
}

// launchSession creates a new agent session using the specified backend (tmux or pty).
func (h *SessionsHandler) launchSession(ctx context.Context, spec launchSpec) (_ map[string]any, retErr error) {
	if msg := launchDirError(spec.WorkDir); msg != "" {
		return nil, errors.New(msg)
	}
	absDir, _ := filepath.Abs(spec.WorkDir)
	agentType, displayName := spec.AgentType, spec.DisplayName
	if agentType == "" {
		agentType = at.Claude
	}
	// A role another running agent already holds on this board gets a numbered name
	// ("Backend Dev 2"), so the two never share a subscription, mentions or read state
	// (#3543). On this Coral's boards only; a remote board's Coral decides its own.
	if spec.BoardName != "" && (spec.BoardServer == "" || spec.BoardServer == "local") {
		displayName = h.uniqueBoardRole(ctx, spec.BoardName, displayName, agentType)
		picked := naming.SubscriberID(displayName, agentType)
		defer func() {
			if retErr != nil {
				h.releaseRoleReservation(spec.BoardName, picked) // the launch failed: the name is free again
			}
		}()
	}
	if err := validateAgentModel(agentType, spec.Model); err != nil {
		return nil, err
	}
	userSettings, err := h.launchSettings(ctx)
	if err != nil {
		return nil, err
	}
	cliPath, err := resolveLaunchCLI(agentType, agent.ResolveCLIPath(userSettings, agentType))
	if err != nil {
		return nil, err
	}
	if err := agent.ValidateAgentType(agentType); err != nil {
		return nil, err
	}
	backend := h.launchPath(spec.Backend)
	folderName := filepath.Base(absDir)
	sessionID := generateUUID()
	sessionName := naming.SessionName(agentType, sessionID)
	logFile := naming.LogFile(h.cfg.LogDir, agentType, sessionID)

	isTerminal := agentType == at.Terminal
	agentImpl := agent.GetAgent(agentType)
	if spec.ResumeSessionID != "" && !isTerminal {
		agent.TryPrepareResume(agentImpl, spec.ResumeSessionID, absDir)
	}
	launchParams := agent.LaunchParams{
		SessionID:       sessionID,
		SessionName:     sessionName,
		ProtocolPath:    h.protocolPath(),
		ResumeSessionID: spec.ResumeSessionID,
		Flags:           spec.Flags,
		WorkingDir:      absDir,
		BoardName:       spec.BoardName,
		BoardLabel:      h.boardLabel(spec.BoardName),
		Role:            naming.SubscriberID(displayName, agentType),
		Prompt:          spec.Prompt,
		PromptOverrides: promptOverrides(userSettings),
		BoardType:       spec.BoardType,
		Capabilities:    spec.Capabilities,
		Tools:           spec.Tools,
		Hooks:           spec.Hooks,
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
		if cmd, err = launchCommand(agentImpl, launchParams); err != nil {
			return nil, err
		}
	}
	if backend == "pty" && h.backend != nil {
		if logFile, err = h.startOnBackend(ctx, spec.BoardName, sessionName, agentType, absDir, sessionID, cmd); err != nil {
			return nil, err
		}
	} else {
		backend = "tmux" // normalize if pty requested but no backend available
		if err := h.startOnTmux(ctx, launchParams, spec.BoardName, sessionName, agentType, absDir, folderName, logFile, cmd); err != nil {
			return nil, err
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
		ResumeFromID:  ptr.NonEmpty(spec.ResumeSessionID),
		Flags:         store.MarshalFlags(spec.Flags),
		Prompt:        ptr.NonEmpty(spec.Prompt),
		BoardName:     ptr.NonEmpty(spec.BoardName),
		BoardServer:   ptr.NonEmpty(spec.BoardServer),
		Backend:       ptr.NonEmpty(backend),
		BoardType:     ptr.NonEmpty(spec.BoardType),
		Capabilities:  store.MarshalCapabilities(spec.Capabilities),
		Model:         ptr.NonEmpty(spec.Model),
		ContextWindow: proxy.LookupContextWindow(spec.Model),
		Tools:         store.MarshalCapabilities(spec.Tools),
		PID:           shellPID,
	})
	h.noteLaunchedHooks(ctx, sessionID, agentType) // #3665
	if !isTerminal {
		go func() {
			defer safego.Recover("server/routes/sessions_launch:h.recordCLIVersion")
			h.recordCLIVersion(sessionID, agentType, userSettings)
		}() // #3481
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

// resolveLaunchCLI is the CLI an agent of agentType starts with: cliPath (Settings > CLI Paths), else its binary
// found on PATH or, failing that, in the common install places. A CLI found nowhere is an error naming how to
// install it. A plain terminal has none.
func resolveLaunchCLI(agentType, cliPath string) (string, error) {
	if agentType == at.Terminal {
		return cliPath, nil
	}
	checkBin := cliPath
	if checkBin == "" {
		if info := agent.GetCLIInfo(agentType); info != nil {
			checkBin = info.Binary
		}
	}
	if checkBin == "" {
		return cliPath, nil
	}
	resolved, err := exec.LookPath(checkBin)
	if err == nil {
		if cliPath == "" {
			cliPath = resolved
		}
		return cliPath, nil
	}
	// LookPath failed — try common install locations
	if found := agent.FindCLIInCommonPaths(checkBin); found != "" {
		log.Printf("[launch] %s not on PATH, found at %s", checkBin, found)
		return found, nil
	}
	installCmd, installURL := "", ""
	if info := agent.GetCLIInfo(agentType); info != nil {
		installCmd, installURL = info.InstallCommand, info.InstallURL
	}
	if installURL != "" {
		return "", fmt.Errorf("%s CLI not found. Install it: %s (the vendor's page: %s)", checkBin, installCmd, installURL)
	}
	return "", fmt.Errorf("%s CLI not found. Install it: %s", checkBin, installCmd)
}

// launchPath is the launch path for a requested one.
//
// NOTE: this value selects the LAUNCH PATH, not the terminal a session ends
// up on. "pty" means "go through the TerminalBackend abstraction", which is
// how tmux-backed sessions are launched too — h.backend is a TmuxBackend in
// that case. It is persisted and drives the same branch on wake, so it must
// keep meaning exactly this. Use terminalKind for what actually runs.
// When the server is using a PTY backend, always use PTY regardless of
// what the frontend requests — tmux is not available on this platform.
func (h *SessionsHandler) launchPath(requested string) string {
	if _, isPTY := h.backend.(*ptymanager.PTYBackend); isPTY {
		return "pty"
	}
	if requested != "" {
		return requested
	}
	if h.backend != nil {
		return "pty"
	}
	return "tmux"
}

// startOnBackend starts the agent through the TerminalBackend: a shell first (empty command), then the agent's
// start command typed into it once the shell is ready. This matches the tmux pattern and works cross-platform:
// the shell interprets bash syntax like $(cat ...) correctly. It returns the log file the backend keeps.
func (h *SessionsHandler) startOnBackend(ctx context.Context, team, sessionName, agentType, dir, sessionID, cmd string) (string, error) {
	if err := h.backend.Spawn(sessionName, agentType, dir, sessionID, "", 200, 50); err != nil {
		return "", fmt.Errorf("pty spawn failed: %w", err)
	}
	logFile := h.backend.LogPath(sessionName) // PTY backend manages its own log file
	if cmd == "" {
		return logFile, nil
	}
	log.Printf("[launch] pty session=%s agent=%s cmd=%s", sessionName, agentType, cmd)
	if pb, ok := h.backend.(*ptymanager.PTYBackend); ok {
		if !pb.WaitReady(sessionName, 5*time.Second) {
			log.Printf("[launch] pty shell not ready after 5s, sending anyway: %s", sessionName)
		}
	} else {
		time.Sleep(500 * time.Millisecond)
	}
	if err := h.startAgentCLI(ctx, team, sessionName, cmd); err != nil {
		return "", fmt.Errorf("pty launch command failed: %w", err)
	}
	return logFile, nil
}

// startOnTmux starts the agent in a new tmux session: the session (tmux checked first, on the real tmux
// terminal), Coral's environment in it, its log and title, then the start command (none for a plain terminal).
func (h *SessionsHandler) startOnTmux(ctx context.Context, params agent.LaunchParams, team, sessionName, agentType, dir, folderName, logFile, cmd string) error {
	// Only check real tmux availability when the terminal is the real
	// tmux terminal — tests inject a mock that should bypass this.
	tmuxTerm, isTmux := h.terminal.(*ptymanager.TmuxSessionTerminal)
	if isTmux {
		tmuxPath, ok := tmux.IsAvailable()
		slog.Info("launch tmux availability",
			"available", ok,
			"resolved_path", tmuxPath,
			"client_tmux_bin_before", tmuxTerm.Client().TmuxBin,
			"socket", tmuxTerm.Client().SocketPath,
			"path", os.Getenv("PATH"),
		)
		if !ok {
			return fmt.Errorf("tmux is required to launch agents but was not found on this system. Install tmux (e.g. 'brew install tmux' on macOS), then try again")
		}
		if tmuxPath != "" && tmuxTerm.Client().TmuxBin != tmuxPath {
			slog.Info("launch updating tmux client binary",
				"from", tmuxTerm.Client().TmuxBin,
				"to", tmuxPath,
			)
			tmuxTerm.Client().TmuxBin = tmuxPath
		}
	}

	ptymanager.ResetAgentLog(logFile) // 0600, never through a link (#3601, W17)
	slog.Info("launch creating tmux session",
		"session_name", sessionName,
		"working_dir", dir,
		"terminal_type", fmt.Sprintf("%T", h.terminal),
	)
	if err := h.terminal.CreateSession(ctx, sessionName, dir); err != nil {
		slog.Error("launch tmux CreateSession failed",
			"session_name", sessionName,
			"working_dir", dir,
			"terminal_type", fmt.Sprintf("%T", h.terminal),
			"error", err,
		)
		return fmt.Errorf("tmux new-session failed: %w", err)
	}
	// Put the Coral environment into the tmux session so that anything the
	// user runs by hand in that pane — coral-board especially — reaches the
	// server that launched the agent. Same source as the agent's own env.
	if isTmux {
		for _, kv := range agent.CoralEnv(params) {
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
	h.terminal.StartLogging(ctx, sessionName, logFile)
	// Set pane title using native tmux command (avoids shell echo)
	h.terminal.SetPaneTitle(ctx, sessionName+".0", fmt.Sprintf("%s — %s", folderName, agentType))
	if agentType == at.Terminal {
		return nil
	}
	log.Printf("[launch] tmux session=%s agent=%s cmd=%s", sessionName, agentType, cmd)
	return h.startAgentCLI(ctx, team, sessionName+".0", cmd)
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

// agentCaller reports a request made with an agent session's token.
func agentCaller(r *http.Request) bool {
	_, ok := VerifiedSession(r.Context())
	return ok
}

// limitAgentLaunch is what an agent may ask for when it starts another agent (#4032, Security's
// #4031 finding 1): users govern agents through their CLI's permissions, so an agent must not
// start one with fewer. Only the flags in agentLaunchFlags are kept (an allowlist, so a spelling
// nobody listed is removed too); allowed tools, capability "allow" rules and custom hooks
// (commands the agent's CLI would run itself) are removed. Deny rules, which only narrow, stay.
// The Operator (the dashboard, the command line, the API key) keeps every choice.
func limitAgentLaunch(route string, flags []string, caps *agent.Capabilities, tools []string, hooks map[string]interface{}) ([]string, *agent.Capabilities, []string, map[string]interface{}) {
	clean := keepAgentLaunchFlags(flags)
	var dropped []string
	if len(clean) != len(flags) {
		dropped = append(dropped, "flags")
	}
	if caps != nil && len(caps.Allow) > 0 {
		dropped = append(dropped, "capability allow rules")
		caps = &agent.Capabilities{Deny: caps.Deny}
	}
	if len(tools) > 0 {
		dropped = append(dropped, "allowed tools")
		tools = nil
	}
	if len(hooks) > 0 {
		dropped = append(dropped, "hooks")
		hooks = nil
	}
	if len(dropped) > 0 {
		log.Printf("[%s] an agent's launch asked for %s: removed (only the Operator can start a less-restricted agent)", route, strings.Join(dropped, ", "))
	}
	return clean, caps, tools, hooks
}

// agentLaunchFlags are the only flags an agent's launch keeps (value: the flag takes the next
// word when written without "="). coral-agent launch passes nothing else (--model is its only
// CLI option); everything a CLI could be widened with, in any spelling, is not here.
var agentLaunchFlags = map[string]bool{"--model": true}

// keepAgentLaunchFlags keeps agentLaunchFlags, as "--flag", "--flag=value" or "--flag value",
// and drops every other word. A value that looks like a flag is refused with its flag.
func keepAgentLaunchFlags(flags []string) []string {
	var out []string
	for i := 0; i < len(flags); i++ {
		name, value, hasValue := strings.Cut(flags[i], "=")
		takesValue, ok := agentLaunchFlags[name]
		if !ok {
			continue
		}
		if !takesValue {
			out = append(out, flags[i])
			continue
		}
		if !hasValue {
			if i+1 >= len(flags) {
				continue
			}
			i++
			value = flags[i]
		}
		if value == "" || strings.HasPrefix(value, "-") {
			continue
		}
		out = append(out, name, value)
	}
	return out
}

// inSavedTeamTemplate reports whether a team member's definition is exactly an agent entry of
// a team template the Operator saved, after normalising (flags as words, JSON key order), so
// an agent launching that template gets what the Operator chose (#4032).
func inSavedTeamTemplate(settings map[string]string, def launchTeamAgent) bool {
	var templates []struct {
		Agents []struct {
			Name         string                 `json:"name"`
			Role         string                 `json:"role"`
			Prompt       string                 `json:"prompt"`
			Description  string                 `json:"description"`
			Capabilities *agent.Capabilities    `json:"capabilities"`
			AgentType    string                 `json:"agent_type"`
			Model        string                 `json:"model"`
			Flags        json.RawMessage        `json:"flags"`
			Tools        []string               `json:"tools"`
			Hooks        map[string]interface{} `json:"hooks"`
		} `json:"agents"`
	}
	if json.Unmarshal([]byte(settings["saved_team_templates"]), &templates) != nil {
		return false
	}
	want := teamAgentKey(def.Name, def.Prompt, def.AgentType, def.Model, def.Flags, def.Capabilities, def.Tools, def.Hooks)
	for _, t := range templates {
		for _, a := range t.Agents {
			name, prompt := a.Name, a.Prompt
			if name == "" {
				name = a.Role // the dashboard's older template fields
			}
			if prompt == "" {
				prompt = a.Description
			}
			var flags []string
			var asText string
			if json.Unmarshal(a.Flags, &asText) == nil {
				flags = strings.Fields(asText)
			} else if json.Unmarshal(a.Flags, &flags) != nil {
				continue
			}
			if teamAgentKey(name, prompt, a.AgentType, a.Model, flags, a.Capabilities, a.Tools, a.Hooks) == want {
				return true
			}
		}
	}
	return false
}

// teamAgentKey is a team member's definition in one canonical JSON form.
func teamAgentKey(name, prompt, agentType, model string, flags []string, caps *agent.Capabilities, tools []string, hooks map[string]interface{}) string {
	var words []string
	for _, f := range flags {
		words = append(words, strings.Fields(f)...)
	}
	if caps.IsEmpty() {
		caps = nil
	}
	if len(tools) == 0 {
		tools = nil
	}
	if len(hooks) == 0 {
		hooks = nil
	}
	b, _ := json.Marshal([]any{name, prompt, agentType, model, words, caps, tools, hooks}) // maps marshal with sorted keys
	return string(b)
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

// maxLaunchTeamAgents is the most agents one launch-team request may start (#4602).
const maxLaunchTeamAgents = 32
