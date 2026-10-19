// A board's groups and their members (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ListGroups returns all groups for a project with member counts.
// GET /api/board/{project}/groups
func (h *BoardHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	groups, err := h.bs.ListGroups(r.Context(), project)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(groups))
}

// ListGroupMembers returns subscriber IDs in a group.
// GET /api/board/{project}/groups/{groupID}/members
func (h *BoardHandler) ListGroupMembers(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	groupID := chi.URLParam(r, "groupID")
	members, err := h.bs.ListGroupMembers(r.Context(), project, groupID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(members))
}

// AddGroupMember adds a subscriber to a group.
// POST /api/board/{project}/groups/{groupID}/members
func (h *BoardHandler) AddGroupMember(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	groupID := chi.URLParam(r, "groupID")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	subscriberID := body.SubscriberID
	if subscriberID == "" {
		errBadRequest(w, "subscriber_id required")
		return
	}
	if err := h.bs.AddToGroup(r.Context(), project, groupID, subscriberID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveGroupMember removes a subscriber from a group.
// DELETE /api/board/{project}/groups/{groupID}/members/{subscriberID}
func (h *BoardHandler) RemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	groupID := chi.URLParam(r, "groupID")
	subscriberID := chi.URLParam(r, "sessionID") // URL param name kept for route compat
	removed, err := h.bs.RemoveFromGroup(r.Context(), project, groupID, subscriberID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !removed {
		errNotFound(w, "member not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
