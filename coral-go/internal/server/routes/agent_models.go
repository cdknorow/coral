package routes

import (
	"context"
	"errors"
	"net/http"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/agentmodels"
	"github.com/cdknorow/coral/internal/httputil"
)

// newAgentModels finds each CLI the way a launch does (Settings > CLI Paths, then PATH).
func (h *SystemHandler) newAgentModels() *agentmodels.Service {
	return agentmodels.NewService(func(agentType string) (string, error) {
		settings, err := h.ss.GetSettingsForCLI(context.Background())
		if err != nil {
			return "", err // never ask the CLI found on PATH instead of the configured one (#4350)
		}
		return agent.FindCLI(settings, agentType)
	})
}

// GetAgentCLIModels returns the models an agent type's CLI offers its user (#4065), from the
// cache: it never runs a CLI itself. A missing or old list is fetched in the background
// ("refreshing": true); call again for it. Until the CLI has listed its own, the curated list
// GET /api/agent-models serves is returned, with "source": "builtin".
// GET /api/agents/models?type=claude|codex|agy|antigravity|gemini|pi
func (h *SystemHandler) GetAgentCLIModels(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	if agentmodels.Family(typ) == "" {
		errBadRequest(w, "type must be one of claude, codex, agy, antigravity, gemini, pi")
		return
	}
	writeJSON(w, http.StatusOK, h.models.Get(typ))
}

// RefreshAgentCLIModels fetches an agent type's list now, in the background (the Operator's
// Refresh): at most once every 30 seconds per CLI. It answers with the cached list.
// POST /api/agents/models/refresh?type=...
func (h *SystemHandler) RefreshAgentCLIModels(w http.ResponseWriter, r *http.Request) {
	typ := r.URL.Query().Get("type")
	if agentmodels.Family(typ) == "" {
		errBadRequest(w, "type must be one of claude, codex, agy, antigravity, gemini, pi")
		return
	}
	res, err := h.models.Refresh(typ)
	if errors.Is(err, agentmodels.ErrTooSoon) {
		httputil.WriteActionLimit(w, 30, "models_refresh_limit", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}
