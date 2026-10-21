package routes

// Coral Hub's owner reads one team's cross-team switch state, for the hub's guided setup (#5636): GET /api/board/{project}/cross-team/settings. A row of the owner table (tunnel.OwnerHubOnly:
// the Coral Hub owner's alone, a request from this computer or anyone else is refused before this runs). It answers about THIS team and this computer's lock and nothing else: no other team's
// label or id, no list of links, no counts. The owner on this computer reads the same from cross-team-state.

import (
	"context"
	"net/http"

	"github.com/subgentic/coral/internal/board"
)

// lastChangedOf is who changed each of a team's two switches last, by key: through Coral Hub (the hub user) or on this computer. A switch never changed is absent.
func (h *BoardHandler) lastChangedOf(ctx context.Context, project string) map[string]any {
	out := map[string]any{}
	if last, err := h.bs.LastSwitchChanges(ctx, project); err == nil {
		for key, c := range last {
			item := map[string]any{"via": c.Via, "at": c.At, "on": c.New}
			if c.Via == board.SwitchViaHub {
				item["by"] = c.Who
			}
			out[key] = item
		}
	}
	return out
}

// GetCrossTeamSettingsForHub answers the Coral Hub owner: the team's two switches, whether the lock lets the hub change them (a lock that cannot be read reads as no), who changed each switch last, and
// whether the team has a generated id and so can be linked at all. 404 (the same words as the switch routes) for a team that does not exist.
// GET /api/board/{project}/cross-team/settings
func (h *BoardHandler) GetCrossTeamSettingsForHub(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if refuseNonOwner(w, r) {
		return
	}
	project := urlParam(r, "project")
	ctx := r.Context()
	if exists, err := h.bs.BoardExists(ctx, project); err != nil {
		errInternalServer(w, err.Error())
		return
	} else if !exists {
		writeError(w, http.StatusNotFound, "not_found", "no such team")
		return
	}
	settings, err := h.bs.GetBoardSettings(ctx, project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cross_team_messaging": settings.CrossTeamMessaging,
		"cross_team_artifacts": settings.CrossTeamArtifacts,
		"hub_may_change":       h.hubMayChange(ctx),
		"last_changed":         h.lastChangedOf(ctx, project),
		"linkable":             board.IsGeneratedBoardID(project),
	})
}
