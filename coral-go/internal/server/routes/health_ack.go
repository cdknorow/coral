package routes

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/subgentic/coral/internal/board"
)

// healthItemIDRe is the id the check prints for an item (teamhealth.ItemID).
var healthItemIDRe = regexp.MustCompile(`^[0-9a-f]{8}$`)

// AckHealthItem marks an item of the orchestrator's check handled: it stays off the list until
// its state changes (#4175 step 3). POST /api/board/{project}/health/ack {"item": "<id>"}. The
// team's orchestrator (by its own token) or the Operator only (Security's #4176 rule 19).
func (h *BoardHandler) AckHealthItem(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	who, ok := h.healthAdmin(r, project)
	if !ok {
		errForbidden(w, "only the team's orchestrator or the Operator can acknowledge a health check item")
		return
	}
	var body struct {
		Item string `json:"item"`
	}
	if err := decodeJSON(r, &body); err != nil || !healthItemIDRe.MatchString(body.Item) {
		errBadRequest(w, "item: the 8-character id the check shows as [ack <id>]")
		return
	}
	switch err := h.bs.AckHealthItem(r.Context(), project, body.Item, who, time.Now()); {
	case errors.Is(err, board.ErrNoSuchHealthItem):
		errNotFound(w, err.Error())
	case err != nil:
		errInternalServer(w, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": body.Item})
	}
}
