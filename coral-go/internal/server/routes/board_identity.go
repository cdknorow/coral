package routes

import (
	"net/http"
	"time"
)

// The two headers a board's read replies carry (#5435, coral-hub's ask): the board's stable id (it survives a rename; the label does not) and when the board was
// created, so Coral Hub can check a shared board's identity on the same round trip as the read. Headers and not body fields because two of the three replies are a bare
// JSON array of messages, which has no place for a field; one rule for all three keeps a reader's code the same. The values are an id and an RFC 3339 UTC time:
// never text from a message or a task.
const (
	BoardIDHeader        = "X-Coral-Board-Id"
	BoardCreatedAtHeader = "X-Coral-Board-Created-At"
)

// setBoardIdentity adds the two headers for project (the board id: BoardKeyResolver has already turned a name into it). A board with no row, or a created_at that is not a
// time, gets the id (when the row exists) and no created-at; nothing here can fail a read.
func (h *BoardHandler) setBoardIdentity(w http.ResponseWriter, r *http.Request, project string) {
	b, err := h.bs.GetBoard(r.Context(), project)
	if err != nil || b == nil {
		return
	}
	w.Header().Set(BoardIDHeader, b.ID)
	if t, err := time.Parse(time.RFC3339, b.CreatedAt); err == nil {
		w.Header().Set(BoardCreatedAtHeader, t.UTC().Format(time.RFC3339))
	}
}
