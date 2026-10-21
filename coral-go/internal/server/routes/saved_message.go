package routes

import (
	"context"
	"net/http"
	"strconv"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/transcriptlink"
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

// SetSavedPresence injects how the list tells whether the bookmarked messages still exist (#5802).
func (h *SystemHandler) SetSavedPresence(board func(ctx context.Context, project string, ids []int64) (map[int64]bool, error), agent func(ctx context.Context, sessionID, agentType string, keys []string) (map[string]bool, bool, bool)) {
	h.savedBoardHas, h.savedAgentHas = board, agent
}

// maxSavedAgentChecks bounds how many agents' transcripts one list reads: the rest are listed unchecked (reason "") and their Open finds out.
const maxSavedAgentChecks = 50

// markSavedReasons sets the Reason of each listed bookmark (#5802): a board's by one query per board, an agent's by one transcript read per agent, never one request per
// bookmark. A source that cannot be asked leaves its bookmarks unmarked (an error is not "gone"). The refs are the stored client text: only a session id reaches a transcript
// lookup (transcriptlink.IsSessionID), any other agent ref is no agent of this computer.
func (h *SystemHandler) markSavedReasons(ctx context.Context, items []store.SavedMessage) {
	boardIDs := map[string][]int64{}
	agentKeys := map[string][]string{}
	agentTypes := map[string]string{}
	var agentOrder []string // the list's order: the newest bookmarks are checked first, and the ones the budget does not reach are the oldest
	for _, it := range items {
		switch it.Kind {
		case "board":
			if id, err := strconv.ParseInt(it.Key, 10, 64); err == nil {
				boardIDs[it.Ref] = append(boardIDs[it.Ref], id)
			}
		case "agent":
			if _, seen := agentKeys[it.Ref]; !seen {
				agentOrder = append(agentOrder, it.Ref)
				agentTypes[it.Ref] = it.AgentType
			}
			agentKeys[it.Ref] = append(agentKeys[it.Ref], it.Key)
		}
	}
	boardHave := map[string]map[int64]bool{}
	if h.savedBoardHas != nil {
		for project, ids := range boardIDs {
			if have, err := h.savedBoardHas(ctx, project, ids); err == nil {
				boardHave[project] = have
			}
		}
	}
	type agentState struct {
		have      map[string]bool
		reachable bool
		checked   bool
	}
	agents := map[string]agentState{}
	if h.savedAgentHas != nil {
		// All the agents' checks share one time budget (#5820): a Codex history of thousands of rollouts is walked to find a session, and a list must not wait for that. The
		// agents not reached, or cut off, are listed unmarked.
		bctx, cancel := context.WithTimeout(ctx, savedCheckBudget)
		defer cancel()
		for _, ref := range agentOrder {
			if len(agents) >= maxSavedAgentChecks || bctx.Err() != nil {
				break
			}
			if !transcriptlink.IsSessionID(ref) {
				agents[ref] = agentState{checked: true} // no agent of this computer
				continue
			}
			have, reachable, known := h.savedAgentHas(bctx, ref, agentTypes[ref], agentKeys[ref])
			if known {
				agents[ref] = agentState{have: have, reachable: reachable, checked: true}
			}
		}
	}
	for i := range items {
		it := &items[i]
		switch it.Kind {
		case "board":
			id, err := strconv.ParseInt(it.Key, 10, 64)
			have, asked := boardHave[it.Ref]
			if err != nil {
				it.Reason = savedReasonNotReachable
			} else if asked && !have[id] {
				it.Reason = savedReasonGone
			}
		case "agent":
			st := agents[it.Ref]
			switch {
			case !st.checked:
			case !st.reachable:
				it.Reason = savedReasonNotReachable
			case !st.have[it.Key]:
				it.Reason = savedReasonGone
			}
		}
	}
}

// SavedMessage: GET /api/saved/{id}/message. No query is read.
func (h *SystemHandler) SavedMessage(w http.ResponseWriter, r *http.Request) {
	if refuseNonOwner(w, r) { // through Coral Hub the owner's alone (#5753)
		return
	}
	if refuseUnlessOperator(w, r, "read a saved message") {
		return
	}
	w.Header().Set("Cache-Control", "no-store") // the reply is message text (#5763)
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
		// The stored ref is client text (cleanSavedField only trims it): it reaches the transcript lookups, some of which glob. A Coral session id is a lowercase UUID and nothing
		// else (transcriptlink.IsSessionID): any other ref is no agent of this computer (#5763, Security's SHOULD 2).
		if h.savedAgentText == nil || !transcriptlink.IsSessionID(item.Ref) {
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
	reply := map[string]any{"text": out, "partial": partial, "reason": ""} // reason is always there: "" when the whole text is
	if partial {
		reply["reason"] = savedReasonTooLong
	}
	writeJSON(w, http.StatusOK, reply)
}
