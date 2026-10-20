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
	if !headerSafeID(b.ID) {
		return // a board that predates boards is named by its old label, which can be any text: only a plain one goes in a header (#5436)
	}
	w.Header().Set(BoardIDHeader, b.ID)
	if t, err := time.Parse(time.RFC3339, b.CreatedAt); err == nil {
		w.Header().Set(BoardCreatedAtHeader, t.UTC().Format(time.RFC3339))
	}
}

// headerSafeID reports whether id can be a response header value as it is: 1 to 128 printable ASCII characters, not starting or ending with a space. A new board's id
// (b- and ten letters or digits) always is; a legacy board's label with a control character, a line break or a non-ASCII letter is not, and then the reply has no id
// header (the hub keeps what it had) instead of one the receiver would read differently.
func headerSafeID(id string) bool {
	if id == "" || len(id) > 128 || id[0] == ' ' || id[len(id)-1] == ' ' {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] > 0x7e {
			return false
		}
	}
	return true
}
