package routes

import (
	"net/http"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/board"
)

// GetPromptInspection displays defaults separately from current global overrides.
// It does not edit global role prompts or represent an already running session.
func (h *SystemHandler) GetPromptInspection(w http.ResponseWriter, r *http.Request) {
	settings, err := h.ss.GetSettings(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	project := r.URL.Query().Get("board")
	if project == "" {
		project = "{board_name}"
	}
	role := func(name, key, system, action string) map[string]any {
		return map[string]any{
			"system_default": system, "action_default": action, "override": settings[key],
			"effective_system": agent.BuildBoardSystemPrompt(project, name, "", settings, ""),
			"effective_action": agent.BuildBoardActionPrompt(project, name, "", settings, ""),
			"scope":            "Global role configuration; read-only here",
			"applicability":    "Preview for a new board session, excluding agent-specific base prompts. A non-empty global override replaces both role fragments. The system preview includes the board introduction; the action preview resolves the board placeholder. These are separate launch channels, not one concatenated prompt. Existing sessions are unchanged.",
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"orchestrator": role("Orchestrator", "default_prompt_orchestrator", agent.DefaultOrchestratorSystemPrompt, agent.DefaultOrchestratorActionPrompt),
		"worker":       role("Worker", "default_prompt_worker", agent.DefaultWorkerSystemPrompt, agent.DefaultWorkerActionPrompt),
		"task":         map[string]any{"default_instructions": board.DefaultTaskWorkflowInstructions, "scope": "Shipped task default; read-only here", "applicability": "Stored when a task is created, followed by any per-task additional instructions. Team workflow guidance is appended on first claim. Historical task instructions are not rewritten."},
	})
}
