package routes

import (
	"github.com/subgentic/coral/internal/board"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/store"
)

// TeamsHandler provides HTTP handlers for the teams API.
type TeamsHandler struct {
	ts *store.TeamStore
	bs *board.Store // board labels (#3536)
}

// SetBoardStore lets the team answers carry each team's board label (#3536).
func (h *TeamsHandler) SetBoardStore(bs *board.Store) { h.bs = bs }

// teamBoardLabels maps team names (board ids) to their board labels.
func (h *TeamsHandler) teamBoardLabels(r *http.Request, names []string) map[string]string {
	if h.bs == nil {
		out := make(map[string]string, len(names))
		for _, n := range names {
			out[n] = n
		}
		return out
	}
	return h.bs.BoardLabels(r.Context(), names)
}

// NewTeamsHandler creates a new TeamsHandler.
func NewTeamsHandler(db *store.DB) *TeamsHandler {
	return &TeamsHandler{
		ts: store.NewTeamStore(db),
	}
}

// Store returns the underlying TeamStore for use by other handlers.
func (h *TeamsHandler) Store() *store.TeamStore {
	return h.ts
}

// ListTeams returns all teams with summary info.
// GET /api/teams/all
func (h *TeamsHandler) ListTeams(w http.ResponseWriter, r *http.Request) {
	statusFilter := r.URL.Query().Get("status")
	teams, err := h.ts.ListTeams(r.Context(), statusFilter)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	// Build response with member counts
	type teamSummary struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		BoardLabel  string  `json:"board_label"`
		Status      string  `json:"status"`
		WorkingDir  string  `json:"working_dir"`
		IsWorktree  int     `json:"is_worktree"`
		MemberCount int     `json:"member_count"`
		ActiveCount int     `json:"active_count"`
		CreatedAt   string  `json:"created_at"`
		UpdatedAt   string  `json:"updated_at"`
		StoppedAt   *string `json:"stopped_at,omitempty"`
	}

	names := make([]string, len(teams))
	for i, t := range teams {
		names[i] = t.Name
	}
	labels := h.teamBoardLabels(r, names)
	results := make([]teamSummary, 0, len(teams))
	for _, t := range teams {
		// Count members
		all, _ := h.ts.GetActiveMembers(r.Context(), t.ID)
		// For total count, query all members (not just active)
		full, err := h.ts.GetTeam(r.Context(), t.Name)
		memberCount := 0
		if err == nil && full != nil {
			memberCount = len(full.Members)
		}

		results = append(results, teamSummary{
			ID:          t.ID,
			Name:        t.Name,
			BoardLabel:  labels[t.Name],
			Status:      t.Status,
			WorkingDir:  t.WorkingDir,
			IsWorktree:  t.IsWorktree,
			MemberCount: memberCount,
			ActiveCount: len(all),
			CreatedAt:   t.CreatedAt,
			UpdatedAt:   t.UpdatedAt,
			StoppedAt:   t.StoppedAt,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"teams": results})
}

// GetTeam returns a team by name with full member list.
// GET /api/teams/detail/{name}
func (h *TeamsHandler) GetTeam(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		errBadRequest(w, "team name required")
		return
	}

	team, err := h.ts.GetTeam(r.Context(), name)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if team == nil {
		errNotFound(w, "team not found")
		return
	}

	writeJSON(w, http.StatusOK, struct {
		*store.Team
		BoardLabel string `json:"board_label"`
	}{team, h.teamBoardLabels(r, []string{team.Name})[team.Name]})
}

// DeleteTeam deletes a stopped team.
// DELETE /api/teams/detail/{name}
func (h *TeamsHandler) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		errBadRequest(w, "team name required")
		return
	}

	team, err := h.ts.GetTeam(r.Context(), name)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if team == nil {
		errNotFound(w, "team not found")
		return
	}

	if err := h.ts.DeleteTeam(r.Context(), team.ID); err != nil {
		errBadRequest(w, "can only delete stopped teams")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
