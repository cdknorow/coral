package routes

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
)

var eventQueryParamNames = map[string]bool{"type": true, "q": true, "since": true, "until": true, "sort": true, "limit": true, "cursor": true, "session_id": true}

// historyEventQueryParamNames is the same without session_id: the history route names its session in the path.
var historyEventQueryParamNames = map[string]bool{"type": true, "q": true, "since": true, "until": true, "sort": true, "limit": true, "cursor": true}

// queryEvents answers ListEvents when it is a search (#4745).
func (h *SessionsHandler) queryEvents(w http.ResponseWriter, r *http.Request, name string, sessionID *string, vals map[string][]string) {
	queryAgentEvents(w, r, h.ts, name, sessionID, vals, eventQueryParamNames)
}

// queryAgentEvents is the events search of the live route (an agent's events) and of the history route (a session's): the same
// parameters, reply and Coral Hub rules (#4779).
func queryAgentEvents(w http.ResponseWriter, r *http.Request, ts *store.TaskStore, name string, sessionID *string, vals map[string][]string, allowed map[string]bool) {
	var unknown []string
	for k := range vals {
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		errBadRequest(w, "unknown parameter "+strings.Join(unknown, ", "))
		return
	}
	q, err := store.ParseEventQuery(name, sessionID, store.EventQueryParams{
		Type: vals["type"], Q: vals["q"], Since: vals["since"], Until: vals["until"], Sort: vals["sort"], Limit: vals["limit"], Cursor: vals["cursor"],
	})
	if err != nil {
		if errors.Is(err, store.ErrEventQuery) {
			errBadRequest(w, strings.TrimPrefix(err.Error(), store.ErrEventQuery.Error()+": "))
			return
		}
		errInternalServer(w, err.Error())
		return
	}
	if cloud.Tunneled(r) && q.Text != "" {
		// The reply is redacted after this handler has matched; matching the stored text would let q tell a viewer what the redaction
		// hides, a prefix at a time. So the match is made on what the viewer sees, and a q holding a secret is refused.
		if shownText(q.Text) != q.Text {
			errBadRequest(w, "q can't contain a secret (a token, a key or a password): it would match what the redaction hides")
			return
		}
		q.Shown = shownTextMemoized
	}
	res, err := ts.QueryAgentEvents(r.Context(), q)
	if err != nil {
		errInternalServer(w, "could not search the events")
		return
	}
	out := map[string]any{"events": res.Events, "total": res.Total, "limit": q.Limit}
	if res.NextCursor != "" {
		out["next_cursor"] = res.NextCursor
	}
	if res.PrevCursor != "" {
		out["prev_cursor"] = res.PrevCursor
	}
	if res.Truncated {
		out["truncated"] = true
	}
	writeJSON(w, http.StatusOK, out)
}
