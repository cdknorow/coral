// Launching a team (moved out of sessions_launch.go, #5003; no behaviour change).

package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentmodels"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/tracking"
)

// launchTeamAgent is one member of a team launch.
type launchTeamAgent struct {
	Name         string                 `json:"name"`
	Prompt       string                 `json:"prompt"`
	Capabilities *agent.Capabilities    `json:"capabilities"`
	AgentType    string                 `json:"agent_type"`
	Model        string                 `json:"model"`
	Thinking     string                 `json:"thinking"` // #4969
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
	// HealthMonitor true turns the team's health check on (#4962). The check is off for a new team; false or absent leaves the team's
	// setting as it is, so a relaunch never turns off a check the operator turned on.
	HealthMonitor *bool `json:"health_monitor"`
	WorkingMode   *struct {
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
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// A team is a handful of agents; a launch-team that names hundreds would start that many CLI processes (an agent token may call
	// this, #4602). The larger ceiling on live sessions an agent token started is a separate setting.
	if len(body.Agents) > maxLaunchTeamAgents {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("a team can have at most %d agents per launch", maxLaunchTeamAgents), "code": "too_many_agents", "max": maxLaunchTeamAgents})
		return
	}
	if h.refuseAgentLaunchOverLimit(w, r, len(body.Agents)) {
		return
	}
	for _, a := range body.Agents {
		if mcpGiven(a.MCPServers) {
			errBadRequest(w, fmt.Sprintf("agent %q: %s", a.Name, errMCPNotSupported))
			return
		}
	}
	// Through the Coral Hub, a team with any member that skips permission prompts needs the owner's local switch, checked
	// before anything is written or started (#126 F4). A member that is exactly one of the owner's saved templates may keep
	// its allow rules, tools and hooks; its flags are checked all the same.
	if cloud.Tunneled(r) {
		settings, err := h.ss.GetSettings(r.Context())
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if refuseHubSkip(w, r, h.ss, teamAsksToSkip(body, settings)) {
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
	boardCreated := false // this request made the board (and so takes it back if nobody starts)
	if body.BoardName != "" || body.BoardID != "" {
		key, created, ok := h.launchBoardCreated(w, r, body.BoardID, body.BoardName, body.NewBoard)
		if !ok {
			return
		}
		body.BoardName = key
		boardCreated = created
	}
	h.restoreTeamTemplate(r.Context(), &body)

	memberTypes, skippedMembers := teamMemberTypes(body)
	attempt := tracking.StartLaunch(tracking.KindTeam, tracking.TeamProvider(memberTypes), h.launchBackendLabel(body.Backend), len(body.Agents), false)
	// A launch refused before anything started takes back the board this request made, so fixing the field and launching again with the same name goes through (#6701, UI/UX's #6723).
	takeBoardBack := func() {
		h.rollBackTeamLaunch(context.WithoutCancel(r.Context()), body, 0, "", false, boardCreated)
	}
	refuse := func(category, msg string) {
		attempt.Finish(tracking.OutcomeFailure, category, 0, len(body.Agents))
		takeBoardBack()
		errBadRequest(w, msg)
	}
	if msg := checkTeamRequest(body); msg != "" {
		refuse(tracking.FailInvalidRequest, msg)
		return
	}
	// From here a worktree, a team record and terminals are made: the team launches to its end whatever the request does, with a bound that grows with
	// the team (#5213).
	ctx, cancel := lifecycleCtxN(viaHub(r.Context(), r), len(body.Agents))
	defer cancel()
	workingDir, worktreePath, branchCreated, wtErr := h.createTeamWorktree(ctx, body)
	if wtErr != nil {
		var missing *gitutil.BranchMissingError
		if errors.As(wtErr, &missing) { // the one a dashboard shows under the Base Branch field, with the branches to pick from (#6701)
			attempt.Finish(tracking.OutcomeFailure, tracking.FailWorktreeFailed, 0, len(body.Agents))
			takeBoardBack()
			writeBranchMissing(w, missing)
			return
		}
		refuse(tracking.FailWorktreeFailed, wtErr.Error())
		return
	}
	teamID := h.createTeamRecord(ctx, body, workingDir, worktreePath)

	// Load settings once so we can fall back to per-type default_model_<type>. A read that fails
	// stops the team before any agent starts: the loop below would otherwise start its agents
	// without the configured CLI path (#4350).
	userSettings, err := h.launchSettings(ctx)
	if err != nil {
		attempt.Finish(tracking.OutcomeFailure, tracking.FailSpawnFailed, 0, len(body.Agents))
		h.rollBackTeamLaunch(ctx, body, teamID, worktreePath, branchCreated, boardCreated)
		writeError(w, http.StatusServiceUnavailable, "", err.Error())
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
	var categories []string // the analytics category of each member that failed
	firstFailure := ""
	for _, agentDef := range body.Agents {
		if agentDef.Name == "" {
			continue
		}
		memberType := body.AgentType
		if agentDef.AgentType != "" {
			memberType = agentDef.AgentType
		}
		savedMember := fromAgent && inSavedTeamTemplate(userSettings, agentDef, memberType)
		if fromAgent && !savedMember && agentLaunchUnsupervised(memberType) {
			// An agent does not start a member of a type that never asks (#5901), unless it is exactly one of the owner's saved entries: not started, a setup problem (the category "" makes
			// an all-refused team a 422), as the Hub's refusal of a member is.
			launched = append(launched, map[string]any{"name": agentDef.Name, "error": agentLaunchUnsupervisedMessage(memberType), "code": agentLaunchUnsupervisedCode})
			categories = append(categories, "")
			continue
		}
		if fromAgent && !savedMember {
			agentDef.Flags, agentDef.Capabilities, agentDef.Tools, agentDef.Hooks = limitAgentLaunch("launch-team", agentDef.Flags, agentDef.Capabilities, agentDef.Tools, agentDef.Hooks)
		}
		if code, msg, stopped := h.hubMemberStopped(ctx, r, body, agentDef); stopped {
			// Not started, and a setup problem (the category "" makes an all-refused team a 422), never a server fault (#5839).
			launched = append(launched, map[string]any{"name": agentDef.Name, "error": msg, "code": code})
			categories = append(categories, "")
			continue
		}
		// A member that names no mode, flags or capabilities starts in the CLI's normal confirm mode, the "Default" the team form shows (#6415, Codex Test's #6412): the server no longer
		// widens an empty member to Auto (#5836 still applies to a single launch). What the person chose (flags, capabilities, a saved default) is used as before.
		entry, category := h.launchTeamMember(ctx, body, agentDef, userSettings, workingDir, worktreePath, teamID, false)
		if category != "" && firstFailure == "" {
			firstFailure = category
		}
		if _, bad := entry["error"]; bad {
			categories = append(categories, category)
		}
		if _, bad := entry["error"]; !bad && worktreePath != "" {
			if sid, _ := entry["session_id"].(string); sid != "" {
				h.recordWorktreeRow(ctx, sid, worktreePath)
			}
		}
		launched = append(launched, entry)
	}
	finishTeamLaunch(attempt, launched, skippedMembers, firstFailure, len(body.Agents))

	started := 0
	for _, m := range launched {
		if _, bad := m["error"]; !bad {
			started++
		}
	}
	if started == 0 {
		// Nobody started: an error, not ok:true over an empty team. What this request made (the team record, the worktree, the board
		// when it made it) is taken back, so a retry starts clean.
		h.rollBackTeamLaunch(ctx, body, teamID, worktreePath, branchCreated, boardCreated)
		status, code := http.StatusInternalServerError, "team_launch_failed"
		if len(categories) == 0 {
			status, code = http.StatusBadRequest, "no_named_agents"
		} else if allConfigurationFailures(categories) {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"ok": false, "code": code, "error": teamLaunchFailureMessage(launched),
			"board": body.BoardName, "agents": launched, "rolled_back": true})
		return
	}
	resp := map[string]any{"ok": true, "board": body.BoardName, "board_id": body.BoardName, "board_label": h.boardLabel(body.BoardName), "agents": launched}
	if teamID > 0 {
		resp["team_id"] = teamID
	}
	if started < len(launched) {
		resp["partial"] = true // some members started, the others' entries carry their error
		resp["started"], resp["failed"] = started, len(launched)-started
	}
	writeJSON(w, http.StatusOK, resp)
}

// allConfigurationFailures says whether every failed member failed for a reason in the setup (no CLI found, no terminal backend, a
// folder that is not there): the request can't succeed as it is, a 422. Anything else is the server's (500).
func allConfigurationFailures(categories []string) bool {
	for _, c := range categories {
		switch c {
		case tracking.FailProviderMissing, tracking.FailProviderUnrunnable, tracking.FailBackendUnavailable, tracking.FailWorkdirUnavailable:
		case "": // a saved default model that is not a model ID (launchTeamMember gives that failure no analytics category): a Settings problem
		default:
			return false
		}
	}
	return len(categories) > 0
}

// teamLaunchFailureMessage names each member's error, for a launch in which none started.
func teamLaunchFailureMessage(launched []map[string]any) string {
	var parts []string
	for _, m := range launched {
		if e, bad := m["error"]; bad {
			parts = append(parts, fmt.Sprintf("%v: %v", m["name"], e))
		}
	}
	if len(parts) == 0 {
		return "no agent in the team has a name"
	}
	return "no agent in the team started (" + strings.Join(parts, "; ") + ")"
}

// rollBackTeamLaunch takes back what a team launch that started nobody made: its team record, its worktree, and the board when this
// request created it and nothing else has used it since (a board that was already there, or that holds a message, a task or a member, is never deleted). Failures are logged. What
// restoreTeamTemplate wrote (knowledge files, working mode, presets) is not taken back: it goes with a board this request created, and on a
// board that was already there it is the template the Operator asked to restore.
func (h *SessionsHandler) rollBackTeamLaunch(ctx context.Context, body launchTeamRequest, teamID int64, worktreePath string, branchCreated, boardCreated bool) {
	if teamID > 0 && h.teamStore != nil {
		// DeleteTeam removes only a stopped team, and this one has no member to stop.
		err := h.teamStore.UpdateTeamStatus(ctx, teamID, "stopped")
		if err == nil {
			err = h.teamStore.DeleteTeam(ctx, teamID)
		}
		if err != nil {
			log.Printf("[launch-team] could not remove the empty team record %d: %v", teamID, err)
		}
	}
	if worktreePath != "" {
		if out, err := gitutil.Command(ctx, body.WorkingDir, "worktree", "remove", "--force", "--", worktreePath).CombinedOutput(); err != nil {
			log.Printf("[launch-team] could not remove the empty team's worktree %s: %v: %s", logsafe.Dir(worktreePath), err, strings.TrimSpace(string(out)))
		} else if branchCreated {
			// The branch this request made; one that was already there (a previous run's work) stays.
			branch := gitutil.TeamBranch(body.BoardName)
			if out, err := gitutil.Command(ctx, body.WorkingDir, "branch", "-D", "--", branch).CombinedOutput(); err != nil {
				log.Printf("[launch-team] could not remove the empty team's branch %s: %v: %s", branch, err, strings.TrimSpace(string(out)))
			}
		}
	}
	if boardCreated && h.bs != nil && body.BoardName != "" {
		// Only while the board still holds nothing this launch did not make: another launch, or an agent that joined, between the failure and here
		// (a member joins a board by itself) means the board is theirs now (#5762).
		deleted, err := h.bs.DeleteProjectIfUnused(ctx, body.BoardName)
		switch {
		case err != nil:
			log.Printf("[launch-team] could not remove the empty team's board %s: %v", body.BoardName, err)
		case !deleted:
			log.Printf("[launch-team] kept the board %s of the team that started nobody: something else joined or used it meanwhile", body.BoardName)
		}
	}
}

// teamAsksToSkip reports whether any member of a team launch asks to skip permission prompts, with the flags
// launchTeamMember gives it (its own, or the team's when it has none): flags that do (agent.SkipsPermissionPrompts), for
// every member; or capability allow rules, tools or hooks, unless the member is exactly an agent of a team template the
// owner saved (inSavedTeamTemplate, every field). The built-in templates are not the owner's choice: their allow rules
// count (#126 F4, the Orchestrator's decision). One member is enough to refuse the team.
func teamAsksToSkip(body launchTeamRequest, settings map[string]string) bool {
	for _, def := range body.Agents {
		if memberAsksToSkip(body, def, settings) {
			return true
		}
	}
	return false
}

// memberAsksToSkip is teamAsksToSkip for one member.
func memberAsksToSkip(body launchTeamRequest, def launchTeamAgent, settings map[string]string) bool {
	agentType, flags := body.AgentType, def.Flags
	if def.AgentType != "" {
		agentType = def.AgentType
	}
	if len(flags) == 0 {
		flags = body.Flags
	}
	if agent.SkipsPermissionPrompts(agentType, flags) {
		return true
	}
	return hubWidens(def.Capabilities, def.Tools, def.Hooks) && !inSavedTeamTemplate(settings, def, agentType)
}

// hubMemberStopped is the check of one member of a team launch that came through Coral Hub, made when it is its turn (#5839): the owner may have turned
// the switch off, or lowered the mode, since the request was admitted, and the team takes seconds to minutes to start. A member that skips permission
// prompts is then not started: its entry carries the refusal's code and message, as the request would have been refused. Members that keep the prompts are
// not affected (the request was admitted at Launch, as for every route). The settings are read now, not taken from the start of the request.
func (h *SessionsHandler) hubMemberStopped(ctx context.Context, r *http.Request, body launchTeamRequest, def launchTeamAgent) (code, msg string, stopped bool) {
	if !cloud.Tunneled(r) {
		return "", "", false
	}
	settings, err := h.ss.GetSettings(ctx)
	if err != nil {
		return "hub_settings_unreadable", "the settings could not be read to check this member: " + err.Error(), true // fail closed
	}
	code, msg, refused, err := hubSkipRefusal(ctx, r, h.ss, memberAsksToSkip(body, def, settings))
	if err != nil {
		return "hub_settings_unreadable", "the settings could not be read to check this member: " + err.Error(), true
	}
	return code, msg, refused
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
	if body.HealthMonitor != nil && *body.HealthMonitor && h.bs != nil {
		if err := h.bs.SetHealthOn(ctx, body.BoardName, true); err != nil {
			log.Printf("[launch-team] failed to turn on the health check for team %s: %v", body.BoardName, err)
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
		memberType := a.AgentType
		if memberType == "" {
			memberType = body.AgentType
		}
		if memberType == "" {
			memberType = at.Claude
		}
		if t := strings.TrimSpace(a.Thinking); t != "" && !agentmodels.ValidThinking(memberType, t) {
			return fmt.Sprintf("agent %q: %s", a.Name, errInvalidThinking(memberType))
		}
	}
	return ""
}

// recordWorktreeRow gives an agent launched into a team worktree (a team launch's member, a resurrected one) its git row at once (its branch, that it is a worktree, its commit), instead of after the git poller's next
// pass (20 s or more): the dashboard's Kill Team and Kill Session confirmations name the worktree from it, and a person must not be asked to confirm a stop blind (#6817, UI Accessibility's #6806). The branch is
// what git says the worktree is on. The poller refreshes the row as before; a failure here is only logged.
func (h *SessionsHandler) recordWorktreeRow(ctx context.Context, sessionID, worktreePath string) {
	if sessionID == "" || h.gs == nil || h.ss == nil {
		return
	}
	ls, err := h.ss.GetLiveSession(ctx, sessionID)
	if err != nil || ls == nil {
		return
	}
	snap := &store.GitSnapshot{AgentName: ls.AgentName, AgentType: ls.AgentType, WorkingDirectory: worktreePath, SessionID: &sessionID, Worktree: true}
	if out, err := gitutil.Command(ctx, worktreePath, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		snap.Branch = strings.TrimSpace(string(out))
	}
	if out, err := gitutil.Command(ctx, worktreePath, "log", "-1", "--format=%H%n%s").Output(); err == nil {
		hash, subject, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		snap.CommitHash, snap.CommitSubject = hash, subject
	}
	if err := h.gs.UpsertGitSnapshot(ctx, snap); err != nil {
		log.Printf("[worktree] could not record the git row for %s: %v", ls.AgentName, err)
	}
}

// createTeamWorktree makes the team's git worktree when it asks for one (branch coral-team/<board> from its base
// branch, the repository's current branch by default; the branch reused when a previous run left it), and returns the folder its agents start
// in, the worktree ("" for none), or why it failed.
func (h *SessionsHandler) createTeamWorktree(ctx context.Context, body launchTeamRequest) (workingDir, worktreePath string, branchCreated bool, failure error) {
	if !body.Worktree || body.WorkingDir == "" {
		return body.WorkingDir, "", false, nil
	}
	// The base branch: the one the request names, else the repository's current branch (not a "main" the repository may not have, #6701).
	branch, err := gitutil.ResolveBase(ctx, body.WorkingDir, body.BaseBranch)
	if err != nil {
		return "", "", false, err
	}
	// The board's name is one folder name under worktrees/: never a path out of it (#5305).
	if !background.ValidKnowledgeTeam(body.BoardName) {
		return "", "", false, errors.New("the board name cannot name a worktree folder")
	}
	worktreeDir := filepath.Join(h.cfg.CoralDir(), "worktrees")
	os.MkdirAll(worktreeDir, 0755)
	worktreePath = filepath.Join(worktreeDir, body.BoardName)
	worktreeBranch := gitutil.TeamBranch(body.BoardName) // a board name git refuses in a branch name (a space, "~", "..") is slugged: the folder keeps the name (#6701)
	branchCreated, err = gitutil.AddTeamWorktree(ctx, body.WorkingDir, worktreePath, worktreeBranch, branch)
	if err != nil {
		return "", "", false, err
	}
	log.Printf("[launch-team] created worktree at %s (branch %s from %s)", logsafe.Dir(worktreePath), worktreeBranch, branch)
	return worktreePath, worktreePath, branchCreated, nil
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
func (h *SessionsHandler) launchTeamMember(ctx context.Context, body launchTeamRequest, def launchTeamAgent, userSettings map[string]string, workingDir, worktreePath string, teamID int64, autoByDefault bool) (map[string]any, string) {
	agentType := body.AgentType
	if def.AgentType != "" {
		agentType = def.AgentType
	}
	effectiveModel := defaultModelFromSettings(userSettings, agentType, def.Model)
	agentFlags := def.Flags
	memberPermMode := "" // the mode the team's flags stand for, handed on so that it is the one stored (below)
	if len(agentFlags) == 0 {
		permMode := agent.PermissionModeFromFlags(body.Flags)
		cleanFlags := stripAgentPermissionFlags(body.Flags)
		// The team's mode is every member's own unless it names one (#6425): capabilities do not replace it. A member with none gets the mode's flags with the rest of its flags; one
		// with capabilities gets the mode as its permission mode, which the agent's command builder applies with the capabilities only narrowing it (Claude keeps the lists apart).
		carried, _ := agent.SplitPermissionFlags(body.Flags)
		switch {
		case permMode == "" && len(carried) > 0 && def.Capabilities.IsEmpty():
			cleanFlags = append(cleanFlags, carried...) // flags nobody here can read as a mode are handed on, never dropped (#6423)
		case permMode != "" && permMode != "default" && def.Capabilities.IsEmpty():
			cleanFlags = append(cleanFlags, agent.PermissionFlagsForAgent(agentType, permMode)...)
			memberPermMode = permMode
		case permMode != "" && permMode != "default":
			memberPermMode = permMode
		}
		agentFlags = cleanFlags
	}
	if effectiveModel != "" && !agentmodels.ValidModelValue(effectiveModel) {
		// Only a saved default can get here (each member's own model was checked above).
		return map[string]any{"name": def.Name, "error": "the default model in Settings: " + errInvalidModel}, ""
	}
	if modelArgs, _ := agentmodels.ModelArgs(effectiveModel); modelArgs != nil { // checked above
		agentFlags = append(agentFlags, modelArgs...)
	}
	effectiveThinking := defaultThinkingFromSettings(userSettings, agentType, def.Thinking)
	result, err := h.launchSession(ctx, launchSpec{
		WorkDir: workingDir, AgentType: agentType, DisplayName: def.Name, Flags: agentFlags,
		Prompt:    InjectKnowledgeIntoPrompt(h.cfg.CoralDir(), body.BoardName, def.Name, def.Prompt),
		BoardName: body.BoardName, BoardServer: body.BoardServer, Backend: body.Backend, BoardType: body.BoardType,
		Model: effectiveModel, Thinking: effectiveThinking, Capabilities: def.Capabilities, Tools: def.Tools, Hooks: def.Hooks,
		// The member's flags carry the mode as the expanded flags of its type (--sandbox workspace-write -a never for Codex's auto), which PermissionModeFromFlags does not read
		// back (it never widens one type's flags into a mode), so the mode stored for it was the default and a team reset relaunched it with no permission flags (#5251).
		PermissionMode: memberPermMode,
		AutoByDefault:  autoByDefault,
	})
	if err != nil {
		log.Printf("[launch-team] failed to launch agent %s: %v", def.Name, err)
		return map[string]any{"name": def.Name, "error": launchErrText(ctx, err)}, classifyLaunchError(err)
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

// maxLaunchTeamAgents is the most agents one launch-team request may start (#4602).
const maxLaunchTeamAgents = 32
