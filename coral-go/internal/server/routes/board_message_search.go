package routes

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cloud"
)

var messageSearchParamNames = map[string]bool{"q": true, "from": true, "mentions": true, "since": true, "until": true, "sort": true, "limit": true, "cursor": true}

// SearchMessages is GET /api/board/{project}/messages/search (#4745), the search the board page's box and its filters use:
//
//	?q=<text>&from=<name>[,<name>]&mentions=<member>&since=<RFC 3339>&until=<RFC 3339>&sort=newest|oldest&limit=N&cursor=C
//
// q is a plain, case-insensitive substring of the message text (% and _ are ordinary characters, at most 200 characters); from the
// sender's id or name; mentions the messages that tag that member (@name, @all, @notify-all); since is inclusive and until exclusive. The
// same parameter names and reply shape as the Team Artifacts query: {"messages": [...], "total": N, "next_cursor": "...", "prev_cursor":
// "...", "limit": N}, the messages as /messages/all gives them. It pages both ways: next_cursor is the page after the last message,
// prev_cursor the page before the first (each only when there is one), and sort=oldest with limit=1 is "jump to the first". total is every
// match, whatever the page; "truncated": true says the search stopped at its bound (200,000 messages or 64 MiB of text) and so did the
// total and the pages. An unknown or malformed parameter is a 400 naming it. Through Coral Hub the match is made on what the viewer sees,
// and a q, from or mentions holding a secret is refused (the redaction would hide what it matched).
func (h *BoardHandler) SearchMessages(w http.ResponseWriter, r *http.Request) {
	vals, perr := url.ParseQuery(r.URL.RawQuery)
	if perr != nil {
		errBadRequest(w, "the query string is not valid (no ';', and %-escapes must be two hex digits)")
		return
	}
	var unknown []string
	for k := range vals {
		if !messageSearchParamNames[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		errBadRequest(w, "unknown parameter "+strings.Join(unknown, ", "))
		return
	}
	q, err := board.ParseMessageQuery(board.MessageQueryParams{
		Q: vals["q"], From: vals["from"], Mentions: vals["mentions"], Since: vals["since"], Until: vals["until"],
		Sort: vals["sort"], Limit: vals["limit"], Cursor: vals["cursor"],
	})
	if err != nil {
		if errors.Is(err, board.ErrMessageQuery) {
			errBadRequest(w, strings.TrimPrefix(err.Error(), board.ErrMessageQuery.Error()+": "))
			return
		}
		errInternalServer(w, err.Error())
		return
	}
	q.Project = urlParam(r, "project")
	if cloud.Tunneled(r) {
		// The reply is redacted after this handler has matched: matching the stored text, or a sender's stored name, would let a viewer
		// learn what the redaction hides, a prefix at a time. So the match is made on what the viewer sees, and a value that holds a secret
		// (one the redactor would change) can match nothing visible: refuse it.
		for name, vs := range map[string][]string{"q": {q.Text}, "from": q.From, "mentions": {q.Mentions}} {
			for _, v := range vs {
				if v != "" && shownText(v) != v {
					errBadRequest(w, name+" can't contain a secret (a token, a key or a password): it would match what the redaction hides")
					return
				}
			}
		}
		q.Shown = shownTextMemoized
	}
	res, err := h.bs.QueryMessages(r.Context(), q)
	if err != nil {
		errInternalServer(w, "could not search the messages")
		return
	}
	out := map[string]any{"messages": res.Messages, "total": res.Total, "limit": q.Limit}
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
