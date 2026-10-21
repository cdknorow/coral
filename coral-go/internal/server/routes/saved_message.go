package routes

import (
	"context"
	"net/http"
	"strconv"

	"github.com/subgentic/coral/pkg/redact"
)

// "Read the whole message" for a bookmark (#5753, Shared Inbox, Security's conditions 3 and 4): the saved item keeps only a 200-byte redacted snippet, so the
// text is read from its source when asked. GET /api/saved/{id}/message answers {text, partial, reason}: the source is the STORED bookmark's (its kind, ref and
// key), never a ref or key from the request, and the text is redacted and cut to maxSavedMessage. The route is the owner's alone through Coral Hub (the tunnel
// table's owner row) and refused to an agent's token on this computer, like every saved route.

const (
	maxSavedMessage     = 64 << 10  // the text of the reply
	maxSavedMessageRead = 256 << 10 // how much of a source is read to be redacted: a token cut at the end is dropped whole by redact.Cut
)

// The reason a reply has no text or only a part of it.
const (
	savedReasonGone         = "gone"          // the message no longer exists (deleted, or not on that board)
	savedReasonNotReachable = "not_reachable" // this computer holds no transcript of the agent that wrote it
	savedReasonTooLong      = "too_long"      // the text is cut to the cap
)

// SetSavedSources injects how the sources of the bookmarks are read: a board's message by id, an agent's message by session id and key.
func (h *SystemHandler) SetSavedSources(board func(ctx context.Context, project string, id int64) (string, bool, error), agent func(ctx context.Context, sessionID, agentType, key string) (string, bool, bool)) {
	h.savedBoardText, h.savedAgentText = board, agent
}

// SavedMessage: GET /api/saved/{id}/message. No query is read.
func (h *SystemHandler) SavedMessage(w http.ResponseWriter, r *http.Request) {
	if refuseNonOwner(w, r) { // through Coral Hub the owner's alone (#5753)
		return
	}
	if refuseUnlessOperator(w, r, "read a saved message") {
		return
	}
	id, ok := savedID(w, r)
	if !ok {
		return
	}
	item, found, err := h.ss.GetSaved(r.Context(), id)
	if err != nil {
		errInternalServer(w, "could not read the saved message")
		return
	}
	if !found {
		errNotFound(w, "no such saved message")
		return
	}
	var text string
	var have, reachable bool
	switch item.Kind {
	case "board":
		mid, perr := strconv.ParseInt(item.Key, 10, 64)
		if perr != nil || h.savedBoardText == nil {
			writeJSON(w, http.StatusOK, map[string]any{"text": "", "partial": false, "reason": savedReasonNotReachable})
			return
		}
		t, ok, rerr := h.savedBoardText(r.Context(), item.Ref, mid) // the bookmark's own board: an id of another board is not found there
		if rerr != nil {
			writeJSON(w, http.StatusOK, map[string]any{"text": "", "partial": false, "reason": savedReasonNotReachable})
			return
		}
		text, have, reachable = t, ok, true
	case "agent":
		if h.savedAgentText == nil {
			writeJSON(w, http.StatusOK, map[string]any{"text": "", "partial": false, "reason": savedReasonNotReachable})
			return
		}
		text, have, reachable = h.savedAgentText(r.Context(), item.Ref, item.AgentType, item.Key)
	}
	switch {
	case !reachable:
		writeJSON(w, http.StatusOK, map[string]any{"text": "", "partial": false, "reason": savedReasonNotReachable})
		return
	case !have || text == "":
		writeJSON(w, http.StatusOK, map[string]any{"text": "", "partial": false, "reason": savedReasonGone})
		return
	}
	// Redacted, then cut where no token can be split: the source is read only to maxSavedMessageRead (a very long message is cut before the redactor, at a place that drops a
	// token it would cross), and the reply to maxSavedMessage. A text that cannot be checked is not sent.
	partial := false
	if n := redact.Cut(text, maxSavedMessageRead); n < len(text) {
		text, partial = text[:n], true
	}
	red, rerr := requestRedactor(r).Text([]byte(text))
	if rerr != nil {
		errInternalServer(w, "could not check the message for secrets")
		return
	}
	out := string(red)
	if n := redact.Cut(out, maxSavedMessage); n < len(out) {
		out, partial = out[:n], true
	}
	reply := map[string]any{"text": out, "partial": partial}
	if partial {
		reply["reason"] = savedReasonTooLong
	}
	writeJSON(w, http.StatusOK, reply)
}
