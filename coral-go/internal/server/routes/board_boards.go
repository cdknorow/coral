// Deleting, describing and renaming a board (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/subgentic/coral/internal/board"
)

// DeleteBoard deletes a board and everything on it: its messages, tasks, members and settings (Store.DeleteProject, one transaction).
// Not while its agents run (#5043): they would stay live in a sidebar group named with the raw board id, and the cost table would list
// that id. 409 board_has_running_agents says how many and which; kill them or move them first. An agent that is asleep or gone has no
// terminal and does not count. When the running terminals cannot be listed the delete is refused too (503): a check that could not
// be made is not a pass.
// DELETE /api/board/{project}
func (h *BoardHandler) DeleteBoard(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	running, err := h.runningAgentsOn(r.Context(), project)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "running_agents_unknown", "could not check which agents are running on this board; try again")
		return
	}
	if len(running) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": runningAgentsMessage(running), "code": "board_has_running_agents", "agents": running})
		return
	}
	if err := h.bs.DeleteProject(r.Context(), project); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// The in-memory pause goes only once the board is gone: a delete that failed leaves the board as it was, paused or not.
	h.mu.Lock()
	delete(h.paused, project)
	delete(h.sleepPaused, project)
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// runningAgentsOn is the names of the board's active members whose terminal is running now (a member whose agent is asleep, gone or on
// another machine has none here). Nil when the handler has no terminal backend to ask.
func (h *BoardHandler) runningAgentsOn(ctx context.Context, project string) ([]string, error) {
	if h.terminal == nil {
		return nil, nil
	}
	subs, err := h.bs.ListSubscribers(ctx, project)
	if err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return nil, nil
	}
	panes, err := h.terminal.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(panes))
	for _, p := range panes {
		live[p.SessionName] = true
	}
	var names []string
	for _, sub := range subs {
		if sub.SessionName == "" || !live[sub.SessionName] {
			continue
		}
		name := strings.TrimSpace(sub.JobTitle)
		if name == "" {
			name = sub.SubscriberID
		}
		names = append(names, name)
	}
	return names, nil
}

// runningAgentsMessage: "2 agents are still on this board (Orchestrator, QA Engineer): kill them or move them first".
func runningAgentsMessage(names []string) string {
	shown := names
	if len(shown) > 5 {
		shown = append(append([]string{}, shown[:5]...), "…")
	}
	who := strings.Join(shown, ", ")
	if len(names) == 1 {
		return "1 agent is still on this board (" + who + "): kill it or move it first"
	}
	return strconv.Itoa(len(names)) + " agents are still on this board (" + who + "): kill them or move them first"
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
	writeJSON(w, http.StatusOK, map[string]any{"board_id": b.ID, "board_uid": b.UID, "board_label": b.Label, "created_at": b.CreatedAt,
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
