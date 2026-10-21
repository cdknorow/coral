package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentmodels"
	at "github.com/subgentic/coral/internal/agenttypes"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/tracking"
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
		Thinking        string                 `json:"thinking"` // the thinking level to start with ("" = the default for the type, else the CLI's own), #4969
		ResumeSessionID string                 `json:"resume_session_id"`
		Capabilities    *agent.Capabilities    `json:"capabilities"`
		Tools           []string               `json:"tools"`
		MCPServers      json.RawMessage        `json:"mcpServers"` // refused: not supported (#3644)
		Hooks           map[string]interface{} `json:"hooks"`
	}
	if err := decodeJSON(r, &body); err != nil {
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
			body.AgentType = setreg.Enum(settings, setreg.DefaultAgentType)
		}
	}
	if body.ResumeSessionID != "" && !agent.IsResumeID(body.AgentType, body.ResumeSessionID) {
		errBadRequest(w, "resume_session_id is not a valid resume id") // #3582: it goes into file paths
		return
	}
	if h.refuseAgentLaunchOverLimit(w, r, 1) {
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

	// Thinking level: the request's, else the Settings default for the type.
	effectiveThinking, thinkingErr := h.resolveThinking(r.Context(), body.AgentType, body.Thinking)
	if thinkingErr != "" {
		attempt.Finish(tracking.OutcomeFailure, tracking.FailInvalidRequest, 0, 1)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": thinkingErr, "field": "thinking"})
		return
	}

	// Add model flag if resolved to non-empty
	launchFlags := body.Flags
	if modelArgs, _ := agentmodels.ModelArgs(effectiveModel); modelArgs != nil { // checked above
		launchFlags = append(append([]string{}, body.Flags...), modelArgs...)
	}
	// Everything before this could be refused with nothing made; from here the terminal is made and the agent started whatever the request does (#5213).
	lctx, cancel := lifecycleCtx(r.Context())
	defer cancel()
	result, err := h.launchSession(lctx, launchSpec{
		WorkDir: body.WorkingDir, AgentType: body.AgentType, DisplayName: body.DisplayName, ResumeSessionID: body.ResumeSessionID,
		Flags: launchFlags, Prompt: body.Prompt, BoardName: body.BoardName, BoardServer: body.BoardServer, Backend: body.Backend,
		BoardType: body.BoardType, Model: effectiveModel, Thinking: effectiveThinking, Capabilities: body.Capabilities, Tools: body.Tools, Hooks: body.Hooks,
	})
	if err != nil {
		attempt.Finish(tracking.OutcomeFailure, classifyLaunchError(err), 0, 1)
		if refuse, ok := cliPathRefusal(viaHub(r.Context(), r), err); ok { // a setting to fix, not a server fault (#115)
			refuse(w)
			return
		}
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

// errInvalidThinking says which levels a type takes.
func errInvalidThinking(agentType string) string {
	levels := agentmodels.ThinkingLevels(agentType)
	if len(levels) == 0 {
		return "a thinking level cannot be set for " + agentType + " agents yet"
	}
	return "thinking must be one of " + strings.Join(levels, ", ") + " for " + agentType + " agents (or empty for the default)"
}

// resolveThinking is a launch's thinking level: the request's, else the Settings default for the type (default_thinking_<type>).
// A request's level the type does not take is refused (the message is the reply's error); a saved default that no longer fits is
// ignored, as the CLI's own default then applies. Terminal agents have none.
func (h *SessionsHandler) resolveThinking(ctx context.Context, agentType, requested string) (level, refused string) {
	if agentType == "" {
		agentType = at.Claude
	}
	requested = strings.TrimSpace(requested)
	if requested != "" && !agentmodels.ValidThinking(agentType, requested) {
		return "", errInvalidThinking(agentType)
	}
	if requested != "" || agentType == at.Terminal {
		return requested, ""
	}
	settings, err := h.ss.GetSettings(ctx)
	if err != nil {
		return "", ""
	}
	return defaultThinkingFromSettings(settings, agentType, ""), ""
}

// defaultThinkingFromSettings is requested, else the saved default_thinking_<type> when the type takes it.
func defaultThinkingFromSettings(settings map[string]string, agentType, requested string) string {
	if requested = strings.TrimSpace(requested); requested != "" {
		if agentmodels.ValidThinking(agentType, requested) {
			return requested
		}
		return ""
	}
	if agentType == at.Terminal || settings == nil {
		return ""
	}
	if d := setreg.TextFamily(settings, setreg.DefaultThinkingPrefix+agentType); agentmodels.ValidThinking(agentType, d) {
		return d
	}
	return ""
}
