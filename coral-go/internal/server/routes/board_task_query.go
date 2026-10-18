package routes

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
)

var taskQueryParamNames = map[string]bool{"fields": true, "status": true, "assigned_to": true, "board": true, "priority": true,
	"created_by": true, "verification": true, "parked": true, "created_since": true, "updated_since": true, "sort": true, "order": true, "limit": true, "cursor": true}

// queryAllTasks is GET /api/board/tasks with a query (#4540), for clients such as Coral Hub that show their own filtered view:
//
//	?fields=a,b|summary&status=a&status=b&assigned_to=X&board=P&priority=high&created_by=X&verification=verified&parked=true
//	&created_since=<RFC 3339>&updated_since=<RFC 3339>&sort=created_at|updated_at&order=asc|desc&limit=N&cursor=C
//
// It returns false, leaving the request to the plain list, when the query holds nothing but limit. Otherwise the reply is
// {"tasks": [...], "next_cursor": "..."} (next_cursor only when there is another page): the whole task rows as the plain list
// has them, or only the fields named (board.TaskQueryFieldNames; summary is the minimal set without long text). An unknown
// parameter, field or value is a 400 that names it. See board.md.
func (h *BoardHandler) queryAllTasks(w http.ResponseWriter, r *http.Request) bool {
	// r.URL.Query() drops a pair it cannot parse (a ";" or a bad %-escape) without a word, which would answer a mistyped filter with
	// an unfiltered list: parse here and refuse instead.
	q, perr := url.ParseQuery(r.URL.RawQuery)
	if perr != nil {
		errBadRequest(w, "the query string is not valid (no ';', and %-escapes must be two hex digits)")
		return true
	}
	var unknown []string
	onlyLimit := true
	for k := range q {
		if !taskQueryParamNames[k] {
			unknown = append(unknown, k)
		}
		if k != "limit" {
			onlyLimit = false
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		errBadRequest(w, "unknown parameter "+strings.Join(unknown, ", "))
		return true
	}
	if onlyLimit {
		return false
	}
	query, err := board.ParseTaskQuery(board.TaskQueryParams{
		Fields: q["fields"], Status: q["status"], AssignedTo: q["assigned_to"], Board: q["board"], Priority: q["priority"],
		CreatedBy: q["created_by"], Verification: q["verification"], Parked: q["parked"], CreatedSince: q["created_since"], UpdatedSince: q["updated_since"],
		Sort: q["sort"], Order: q["order"], Limit: q["limit"], Cursor: q["cursor"],
	})
	if err != nil {
		if errors.Is(err, board.ErrTaskQuery) {
			errBadRequest(w, strings.TrimPrefix(err.Error(), board.ErrTaskQuery.Error()+": "))
			return true
		}
		errInternalServer(w, err.Error())
		return true
	}
	// Through Coral Hub the reply is redacted after the filter has matched, so an exact match on a name a token makes up would confirm a
	// guess of that token. A filter value the redactor would change cannot match anything the viewer can see: refuse it (a value
	// without a secret in it matches only names without one).
	if cloud.Tunneled(r) {
		for name, vals := range map[string][]string{"assigned_to": query.AssignedTo, "created_by": query.CreatedBy, "board": query.Boards} {
			for _, v := range vals {
				if shownText(v) != v {
					errBadRequest(w, name+" can't contain a secret (a token, a key or a password): it would match what the redaction hides")
					return true
				}
			}
		}
	}
	out := map[string]any{}
	if query.Fields == nil {
		tasks, next, err := h.bs.QueryTaskRows(r.Context(), query)
		if err != nil {
			errInternalServer(w, err.Error())
			return true
		}
		ids := make([]string, len(tasks))
		for i := range tasks {
			ids[i] = tasks[i].BoardID
		}
		labels := h.bs.BoardLabels(r.Context(), ids)
		for i := range tasks {
			tasks[i].BoardLabel = labels[tasks[i].BoardID]
		}
		out["tasks"] = tasks
		if next != "" {
			out["next_cursor"] = next
		}
	} else {
		res, err := h.bs.QueryTasks(r.Context(), query)
		if err != nil {
			errInternalServer(w, err.Error())
			return true
		}
		out["tasks"] = res.Tasks
		if res.NextCursor != "" {
			out["next_cursor"] = res.NextCursor
		}
	}
	writeJSON(w, http.StatusOK, out)
	return true
}
