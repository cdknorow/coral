package routes

import (
	"context"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/querypage"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/redact"
)

// Save for Later (#4510): the Operator bookmarks a message of an agent's chat or of a board, lists the bookmarks (open or done), and clicks one to go
// back to that point of the conversation. The bookmarks are the Operator's own: an agent's token is refused on every route, a person at this computer
// (the dashboard, the signed command line) or through Coral Hub (the tunnel allows the list at View and the changes at Interact) is let in. The snippet
// that is kept is redacted when it is saved, so a token pasted into a message is not stored.

const (
	maxSavedSnippet    = 200
	maxSavedField      = 300
	maxSavedSearchText = 200
	maxSavedAuthorFill = 500 // board bookmarks whose author one request looks up
)

var (
	savedAgentKey = regexp.MustCompile(`^[0-9a-f]{20}$`)
	savedBoardKey = regexp.MustCompile(`^[0-9]{1,18}$`)
)

// SavedList: GET /api/saved?team=T keeps the bookmarks of one team (#4792: its board's messages and the messages of its agents), agent=REF those of one agent (its chat, and with team and author
// both, the messages that author wrote on that team's board); the open and done counts are of the scope too. author needs agent and team. State=open|done (default: both; kind and conv (the session id or the board) narrow it to one conversation, for the buttons' saved state; q (#4745) keeps the bookmarks whose title or snippet holds that text, a plain case-insensitive substring of at most 200 characters; sort=newest|oldest; limit keeps the first N of the sort's order (the newest by default)).
func (h *SystemHandler) SavedList(w http.ResponseWriter, r *http.Request) {
	if refuseNonOwner(w, r) { // through Coral Hub the owner's alone (#5753)
		return
	}
	if refuseUnlessOperator(w, r, "see the saved messages") {
		return
	}
	w.Header().Set("Cache-Control", "no-store") // the bookmarks are the owner's notes (#5763)
	q := r.URL.Query()
	state := q.Get("state")
	if state != "" && state != "open" && state != "done" && state != "all" {
		errBadRequest(w, "state is open, done or all")
		return
	}
	if state == "all" {
		state = ""
	}
	if len(q["q"]) > 1 || len(q["sort"]) > 1 || len(q["team"]) > 1 || len(q["agent"]) > 1 || len(q["author"]) > 1 {
		errBadRequest(w, "q, sort, team, agent and author are given once")
		return
	}
	scope := store.SavedScope{Team: q.Get("team"), Agent: q.Get("agent"), Author: q.Get("author")}
	for _, v := range []string{scope.Team, scope.Agent, scope.Author} {
		if len(v) > maxSavedField || cleanSavedField(v) != v {
			errBadRequest(w, fmt.Sprintf("team, agent and author are at most %d characters, without control characters", maxSavedField))
			return
		}
	}
	if scope.Author != "" && (scope.Agent == "" || scope.Team == "") {
		errBadRequest(w, "author goes with agent and team")
		return
	}
	text := strings.ToLower(strings.TrimSpace(q.Get("q")))
	if len(text) > maxSavedSearchText || querypage.HasControl(q.Get("q")) {
		errBadRequest(w, fmt.Sprintf("q is at most %d characters, without control characters", maxSavedSearchText))
		return
	}
	oldest := false
	switch q.Get("sort") {
	case "", "newest":
	case "oldest":
		oldest = true
	default:
		errBadRequest(w, "unknown sort "+strconv.Quote(q.Get("sort"))+" (newest or oldest)")
		return
	}
	shown := func(s string) string { return s }
	if cloud.Tunneled(r) && text != "" {
		// The reply is redacted after this handler has matched, so the match is made on what the viewer sees and a q holding a secret
		// (one the redactor would change) can match nothing visible: refused (#4745).
		if shownText(text) != text {
			errBadRequest(w, "q can't contain a secret (a token, a key or a password): it would match what the redaction hides")
			return
		}
		shown = shownTextMemoized
	}
	if scope.Author != "" {
		h.fillSavedAuthors(r.Context(), scope.Team)
	}
	items, err := h.ss.ListSaved(r.Context(), state, q.Get("kind"), q.Get("conv"), scope)
	if err != nil {
		errInternalServer(w, "could not read the saved messages")
		return
	}
	if text != "" { // a plain, case-insensitive substring of the title or the snippet
		kept := items[:0:0]
		for _, it := range items {
			if strings.Contains(strings.ToLower(shown(it.Title)), text) || strings.Contains(strings.ToLower(shown(it.Snippet)), text) {
				kept = append(kept, it)
			}
		}
		items = kept
	}
	total := len(items)
	if oldest {
		slices.Reverse(items)
	}
	// ?limit=N keeps the newest N items (the oldest N with sort=oldest; limit=0: only the counts, which the top bar's badge asks for).
	if raw := q.Get("limit"); raw != "" {
		n, perr := strconv.Atoi(raw)
		if perr != nil || n < 0 {
			errBadRequest(w, "limit is a whole number, 0 or more")
			return
		}
		if n < len(items) {
			items = items[:n]
		}
	}
	h.markSavedReasons(r.Context(), items) // only the bookmarks that are listed (limit=0, the badge's count, asks nothing)
	open, done, err := h.ss.SavedCounts(r.Context(), scope)
	if err != nil {
		errInternalServer(w, "could not read the saved messages")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "open": open, "done": done, "total": total})
}

// SetBoardAuthor injects how a board message's author is read (the board's job title for its sender), for the bookmarks saved before the author was kept (#4792): found is false when the message is gone.
func (h *SystemHandler) SetBoardAuthor(fn func(ctx context.Context, project string, id int64) (author string, found bool, err error)) {
	h.boardAuthor = fn
}

// fillSavedAuthors asks the board who wrote each bookmarked message of a team whose author is not known yet, and keeps the answer (a message that is gone is kept as no author).
// An error leaves the rest to the next ask.
func (h *SystemHandler) fillSavedAuthors(ctx context.Context, team string) {
	if h.boardAuthor == nil {
		return
	}
	unknown, err := h.ss.UnknownSavedAuthors(ctx, team)
	if err != nil {
		return
	}
	if len(unknown) > maxSavedAuthorFill {
		unknown = unknown[:maxSavedAuthorFill] // one request never holds on a very old team: the rest waits for the next ask
	}
	for _, it := range unknown {
		id, perr := strconv.ParseInt(it.Key, 10, 64)
		if perr != nil {
			continue
		}
		author, found, aerr := h.boardAuthor(ctx, it.Ref, id)
		if aerr != nil {
			return
		}
		if !found {
			author = ""
		}
		if h.ss.SetSavedAuthor(ctx, it.ID, author) != nil {
			return
		}
	}
}

// savedSnippet is text redacted, one line, and cut to maxSavedSnippet bytes where no token can be split. Text that can't be checked is not kept.
func savedSnippet(text string, rd redact.Redactor) string {
	b, err := rd.Text([]byte(text))
	if err != nil {
		return redact.Marker
	}
	s := strings.Join(strings.Fields(string(b)), " ")
	n := redact.Cut(s, maxSavedSnippet)
	out := s[:n]
	if n < len(s) {
		out += "…"
	}
	return out
}

func cleanSavedField(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxSavedField {
		s = s[:maxSavedField]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// SavedAdd: POST /api/saved {kind, ref, key, agent_type, title, snippet, said_at, team, author}: team is the board an agent was on (a board message's is its board), author who wrote a board message. Saving a saved message again answers the bookmark that exists.
func (h *SystemHandler) SavedAdd(w http.ResponseWriter, r *http.Request) {
	if refuseNonOwner(w, r) { // through Coral Hub the owner's alone (#5763)
		return
	}
	if refuseUnlessOperator(w, r, "save a message") {
		return
	}
	var body struct {
		Kind      string `json:"kind"`
		Ref       string `json:"ref"`
		Key       string `json:"key"`
		AgentType string `json:"agent_type"`
		Title     string `json:"title"`
		Snippet   string `json:"snippet"`
		SaidAt    string `json:"said_at"`
		Team      string `json:"team"`
		Author    string `json:"author"`
	}
	if err := decodeJSONMax(w, r, &body, 32<<10); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	switch body.Kind {
	case "agent":
		if !savedAgentKey.MatchString(body.Key) {
			errBadRequest(w, "key is the message's key (20 hex characters)")
			return
		}
	case "board":
		if !savedBoardKey.MatchString(body.Key) {
			errBadRequest(w, "key is the board message's id")
			return
		}
	default:
		errBadRequest(w, "kind is agent or board")
		return
	}
	ref := cleanSavedField(body.Ref)
	if ref == "" {
		errBadRequest(w, "ref is required")
		return
	}
	m := store.SavedMessage{Kind: body.Kind, Ref: ref, Key: body.Key, AgentType: cleanSavedField(body.AgentType), Title: cleanSavedField(body.Title),
		Snippet: savedSnippet(body.Snippet, requestRedactor(r)), SaidAt: cleanSavedField(body.SaidAt), Team: cleanSavedField(body.Team)}
	if m.Kind == "board" {
		m.Team = ref // a board message belongs to its own board
		m.Author = cleanSavedField(body.Author)
		if h.boardAuthor != nil {
			// The board says who wrote it, not the request: a bookmark labelled as another agent's would show in that agent's view (#4816).
			// Unreadable or gone: no author kept, and it is looked up when a scope asks (a gone message is then kept as no author).
			m.Author = ""
			if id, perr := strconv.ParseInt(body.Key, 10, 64); perr == nil {
				if author, found, aerr := h.boardAuthor(r.Context(), ref, id); aerr == nil && found {
					m.Author = cleanSavedField(author)
				}
			}
		}
	}
	out, err := h.ss.SaveMessage(r.Context(), m)
	if errors.Is(err, store.ErrTooManySaved) {
		errConflict(w, "at most "+strconv.Itoa(store.MaxSavedMessages)+" messages can be saved; remove some first")
		return
	}
	if err != nil {
		errInternalServer(w, "could not save the message")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func savedID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		errBadRequest(w, "id is the saved message's id")
		return 0, false
	}
	return id, true
}

// SavedUpdate: PATCH /api/saved/{id} {"done": bool}: marks it done, or open again.
func (h *SystemHandler) SavedUpdate(w http.ResponseWriter, r *http.Request) {
	if refuseNonOwner(w, r) { // through Coral Hub the owner's alone (#5763)
		return
	}
	if refuseUnlessOperator(w, r, "change a saved message") {
		return
	}
	id, ok := savedID(w, r)
	if !ok {
		return
	}
	var body struct {
		Done *bool `json:"done"`
	}
	if err := decodeJSONMax(w, r, &body, 4096); err != nil || body.Done == nil {
		errBadRequest(w, "send {\"done\": true|false}")
		return
	}
	out, found, err := h.ss.SetSavedDone(r.Context(), id, *body.Done)
	if err != nil {
		errInternalServer(w, "could not change the saved message")
		return
	}
	if !found {
		errNotFound(w, "no such saved message")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// SavedDelete: DELETE /api/saved/{id}.
func (h *SystemHandler) SavedDelete(w http.ResponseWriter, r *http.Request) {
	if refuseNonOwner(w, r) { // through Coral Hub the owner's alone (#5763)
		return
	}
	if refuseUnlessOperator(w, r, "remove a saved message") {
		return
	}
	id, ok := savedID(w, r)
	if !ok {
		return
	}
	found, err := h.ss.DeleteSaved(r.Context(), id)
	if err != nil {
		errInternalServer(w, "could not remove the saved message")
		return
	}
	if !found {
		errNotFound(w, "no such saved message")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}
