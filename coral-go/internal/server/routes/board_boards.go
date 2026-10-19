// Deleting, describing and renaming a board (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"errors"
	"net/http"
	"strings"

	"github.com/cdknorow/coral/internal/board"
)

// DeleteBoard deletes a board and everything on it: its messages, tasks, members and settings (Store.DeleteProject, one transaction).
// DELETE /api/board/{project}
func (h *BoardHandler) DeleteBoard(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	if err := h.bs.DeleteProject(r.Context(), project); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// The in-memory pause goes only once the board is gone: a delete that failed leaves the board as it was, paused or not.
	h.mu.Lock()
	delete(h.paused, project)
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// refuseNewBoardName answers 400 invalid_board_name and returns true when name would start
// a NEW board (no subscriber or message uses it yet) with a name outside board.NewNamePattern
// (#3534) or shaped like a generated id (#3536). A board that already exists keeps working
// under its name, whatever it is.
func refuseNewBoardName(w http.ResponseWriter, r *http.Request, bs *board.Store, name string) bool {
	if bs == nil {
		return false
	}
	problem := bs.LabelProblem(name) // the name rule, and never id-shaped (#3536)
	if problem == "" {
		return false
	}
	if exists, err := bs.BoardExists(r.Context(), name); err == nil && exists {
		return false
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_board_name", "code": "invalid_board_name", "message": problem})
	return true
}

// GET /api/board/{project}: the board's id, label and creation (#3536).
func (h *BoardHandler) GetBoardInfo(w http.ResponseWriter, r *http.Request) {
	b, err := h.bs.GetBoard(r.Context(), urlParam(r, "project"))
	if errors.Is(err, board.ErrBoardNotFound) {
		errNotFound(w, "no such board")
		return
	}
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"board_id": b.ID, "board_label": b.Label, "created_at": b.CreatedAt,
		"created_by": b.CreatedBy, "members": h.bs.BoardMembers(r.Context(), b.ID)})
}

// PATCH /api/board/{project} {"label": "..."}: rename a board (#3536). Only its label
// changes; its id, and everything keyed by it, stays. The new label follows the name rule.
// The Operator's (operatorOnlyRoutes).
func (h *BoardHandler) RenameBoard(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label string `json:"label"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	label := strings.TrimSpace(body.Label)
	id := urlParam(r, "project")
	if problem := h.bs.LabelProblem(label); problem != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_board_name", "code": "invalid_board_name", "message": problem})
		return
	}
	if err := h.bs.RenameBoard(r.Context(), id, label); errors.Is(err, board.ErrBoardNotFound) {
		errNotFound(w, "no such board")
		return
	} else if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"board_id": id, "board_label": label})
}
