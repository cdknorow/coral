package routes

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/secrets"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
)

// Comments on an artifact (#5176, phase 1). Routes under /api/artifacts/{id}/comments, id the artifact's 64-hex digest; every decision about who may do what is commentPolicy's, and who is
// asking is artifactCommentCaller's. Comment text is text: it is redacted when kept (a token pasted into a comment is not stored) and rendered by the client with textContent.

// ArtifactCommentsHubWriteSetting turns on commenting by Coral Hub viewers (off: they read). Until coral-hub settles a stable subject for its users (#5176) this stays off and the tunnel does
// not list the write routes.
const ArtifactCommentsHubWriteSetting = setreg.ArtifactCommentsHubWrite

const (
	maxCommentQuote   = 2 << 10 // bytes of the selected text
	maxCommentContext = 200     // bytes of prefix and of suffix
	maxCommentWrites  = 20      // writes per author a minute
)

var (
	commentBoardRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	// A text-like artifact: what the viewer shows as text. Anything else (images, archives, binaries) takes no comments.
	commentTextTypes = []string{"text/", "application/json", "application/xml", "application/yaml", "application/x-yaml", "application/javascript", "application/x-sh", "application/toml", "application/csv", "image/svg+xml"}
	commentTextExts  = map[string]bool{".md": true, ".markdown": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true, ".csv": true, ".log": true, ".go": true, ".js": true, ".ts": true,
		".py": true, ".rb": true, ".rs": true, ".java": true, ".c": true, ".h": true, ".cpp": true, ".sh": true, ".html": true, ".htm": true, ".css": true, ".xml": true, ".sql": true, ".diff": true, ".patch": true}
)

// commentAnchor is where a comment points: the selected text with a little context to tell repeats apart, and its lines when the client found them; Whole is a comment on the document.
type commentAnchor struct {
	Quote     string `json:"quote"`
	Prefix    string `json:"prefix,omitempty"`
	Suffix    string `json:"suffix,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Whole     bool   `json:"whole,omitempty"`
}

// commentLimiter allows each author maxCommentWrites writes in any minute, in memory; the table of authors is bounded so a stream of new identities cannot grow it.
type commentLimiter struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

func (l *commentLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string][]time.Time{}
	}
	cut := now.Add(-time.Minute)
	kept := l.seen[key][:0]
	for _, t := range l.seen[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= maxCommentWrites {
		l.seen[key] = kept
		return false
	}
	if _, known := l.seen[key]; !known && len(l.seen) >= 4096 {
		for k, ts := range l.seen { // drop authors with nothing in the last minute
			if len(ts) == 0 || !ts[len(ts)-1].After(cut) {
				delete(l.seen, k)
			}
		}
		if len(l.seen) >= 4096 {
			return false
		}
	}
	l.seen[key] = append(kept, now)
	return true
}

// RegisterArtifactComments adds the comment routes.
func (h *SessionsHandler) RegisterArtifactComments(r chi.Router) {
	r.Get("/api/artifacts/{id}/comments", h.listArtifactComments)
	r.Post("/api/artifacts/{id}/comments", h.createArtifactComment)
	r.Patch("/api/artifacts/{id}/comments/{cid}", h.editArtifactComment)
	r.Delete("/api/artifacts/{id}/comments/{cid}", h.deleteArtifactComment)
	r.Post("/api/artifacts/{id}/comments/{cid}/send", h.sendArtifactComment)
	r.Post("/api/artifacts/{id}/comments/{cid}/resolve", h.resolveArtifactComment(true))
	r.Post("/api/artifacts/{id}/comments/{cid}/reopen", h.resolveArtifactComment(false))
	r.Put("/api/artifacts/{id}/comments/lock", h.lockArtifactComments(true))
	r.Delete("/api/artifacts/{id}/comments/lock", h.lockArtifactComments(false))
}

func commentRefuse(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// commentView is one comment as the client sees it. Mine and the can* flags are for the UI only (the server decides again on every write).
type commentView struct {
	ID         int64          `json:"id"`
	ArtifactID string         `json:"artifact_id"`
	Board      string         `json:"board"`
	ParentID   *int64         `json:"parent_id"`
	Anchor     *commentAnchor `json:"anchor"`
	Body       string         `json:"body"`
	Author     commentAuthor  `json:"author"`
	CreatedAt  string         `json:"created_at"`
	EditedAt   *string        `json:"edited_at"`
	ResolvedBy *commentAuthor `json:"resolved_by"`
	ResolvedAt *string        `json:"resolved_at"`
	Deleted    bool           `json:"deleted"`
	Mine       bool           `json:"mine"`
	Can        commentCan     `json:"can"`
}

type commentAuthor struct {
	Kind    string `json:"kind,omitempty"`
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
	Name    string `json:"name,omitempty"`
}

type commentCan struct {
	Reply   bool `json:"reply"`
	Edit    bool `json:"edit"`
	Delete  bool `json:"delete"`
	Resolve bool `json:"resolve"`
}

// commentOwnerLabel is how the owner is named to anyone who is not the owner, and commentYouLabel to the owner themselves: never the operating-system user name or the install's owner id (the subject
// shown for an owner is the fixed word "owner"). When the signed-in Coral Hub account name turns on as the owner's name, it goes where "Owner" is.
const (
	commentOwnerLabel = "Owner"
	commentYouLabel   = "You"
)

// viewAuthor is an author as a given viewer may see it. The owner's real subject (the install id) never leaves the server: it is only compared, for mine.
func viewAuthor(viewer commentCaller, kind, issuer, subject, name string) commentAuthor {
	if kind == commentCallerOwner {
		label := commentOwnerLabel
		if viewer.Kind == commentCallerOwner && viewer.Issuer == issuer && viewer.Subject == subject && subject != "" {
			label = commentYouLabel
		}
		return commentAuthor{Kind: kind, Issuer: issuer, Subject: "owner", Name: label}
	}
	return commentAuthor{Kind: kind, Issuer: issuer, Subject: subject, Name: name}
}

func (h *SessionsHandler) commentView(c commentCaller, t store.ArtifactComment, locked, hubWrite bool) commentView {
	v := commentView{ID: t.ID, ArtifactID: t.ArtifactID, Board: t.Board, ParentID: t.ParentID, Body: t.Body, CreatedAt: t.CreatedAt, EditedAt: t.EditedAt, ResolvedAt: t.ResolvedAt, Deleted: t.DeletedAt != nil,
		Author: viewAuthor(c, t.AuthorKind, t.AuthorIssuer, t.AuthorSubject, t.AuthorName)}
	if t.AnchorJSON != "" {
		var a commentAnchor
		if json.Unmarshal([]byte(t.AnchorJSON), &a) == nil {
			v.Anchor = &a
		}
	}
	if t.ResolvedIssuer != nil && t.ResolvedSubject != nil {
		who := commentAuthor{Issuer: *t.ResolvedIssuer, Subject: *t.ResolvedSubject}
		if who.Issuer == "local" { // the owner: only ever by the fixed word
			who.Subject = "owner"
		}
		v.ResolvedBy = &who
	}
	if !v.Deleted {
		v.Mine = c.sameAuthor(&t)
		v.Can.Reply, _ = commentPolicy(c, commentCreate, nil, locked, hubWrite)
		v.Can.Edit, _ = commentPolicy(c, commentEdit, &t, locked, hubWrite)
		v.Can.Delete, _ = commentPolicy(c, commentDelete, &t, locked, hubWrite)
		v.Can.Resolve, _ = commentPolicy(c, commentResolve, nil, locked, hubWrite)
	}
	return v
}

// commentArtifact checks the id and that the artifact exists and is text-like; it answers the request itself when not and reports false.
func (h *SessionsHandler) commentArtifact(w http.ResponseWriter, r *http.Request, needText bool) (string, bool) {
	id := chi.URLParam(r, "id")
	if len(id) != 64 || strings.ToLower(id) != id {
		errBadRequest(w, "invalid artifact id")
		return "", false
	}
	if _, err := hex.DecodeString(id); err != nil {
		errBadRequest(w, "invalid artifact id")
		return "", false
	}
	if !artifactReadable(r, h.cfg.CoralDir(), id) { // the one read check (#5577): not yours answers as missing
		errNotFound(w, "artifact not found")
		return "", false
	}
	var meta agentArtifactMeta
	data, err := readSmallFile(filepath.Join(h.artifactDir(), id+".json"), 64<<10)
	if err != nil || json.Unmarshal(data, &meta) != nil {
		errNotFound(w, "artifact not found")
		return "", false
	}
	if needText && !commentTextLike(meta) {
		commentRefuse(w, http.StatusUnsupportedMediaType, "not_text", "only text-like artifacts take comments")
		return "", false
	}
	return id, true
}

// readSmallFile reads a sidecar of at most max bytes (a larger file is an error, not a read of all of it).
func readSmallFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(b)) > max {
		err = errors.New("file too large")
	}
	return b, err
}

func commentTextLike(m agentArtifactMeta) bool {
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(m.MediaType, ";", 2)[0]))
	for _, p := range commentTextTypes {
		if strings.HasPrefix(mt, p) {
			return true
		}
	}
	return commentTextExts[strings.ToLower(filepath.Ext(m.Name))]
}

// commentBoards is the set of boards an agent is on, among the boards these comments were made on; the owner and a hub viewer see all (nil).
func (h *SessionsHandler) commentBoards(r *http.Request, c commentCaller, comments []store.ArtifactComment) []string {
	if c.Kind != commentCallerAgent {
		return nil
	}
	boards := []string{}
	seen := map[string]bool{}
	for _, t := range comments {
		if t.Board == "" || seen[t.Board] {
			continue
		}
		seen[t.Board] = true
		if h.bs != nil {
			if on, err := h.bs.SessionSubscribedOn(r.Context(), t.Board, c.Session); err == nil && on {
				boards = append(boards, t.Board)
			}
		}
	}
	return boards
}

func (h *SessionsHandler) hubWriteOn(r *http.Request) bool {
	st, err := h.ss.GetSettings(r.Context())
	if err != nil {
		return false
	}
	return setreg.Bool(st, ArtifactCommentsHubWriteSetting)
}

// dropArtifactComments removes an artifact's comments and its lock when the artifact itself is deleted or pruned: a comment belongs to that exact artifact and has nothing to point at without it.
func (h *SessionsHandler) dropArtifactComments(ctx context.Context, id string) {
	if h.ss == nil {
		return
	}
	if _, err := h.ss.DeleteArtifactComments(ctx, id); err != nil {
		slog.Warn("[artifact comments] could not remove the comments of a deleted artifact", "digest", id, "error", err)
	}
}

// GET /api/artifacts/{id}/comments?state=open|resolved|all&board=B
func (h *SessionsHandler) listArtifactComments(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.commentCallerFor(w, r, commentRead)
	if !ok {
		return
	}
	if refuseNonOwner(w, r) { // through Coral Hub the owner's alone (#5575)
		return
	}
	id, ok := h.commentArtifact(w, r, false)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	switch state {
	case "", "all":
		state = ""
	case "open", "resolved":
	default:
		errBadRequest(w, "state must be open, resolved or all")
		return
	}
	board := r.URL.Query().Get("board")
	if board != "" && !commentBoardRe.MatchString(board) {
		errBadRequest(w, "invalid board")
		return
	}
	all, err := h.ss.ListArtifactComments(r.Context(), id, state, nil)
	if err != nil {
		errInternalServer(w, "could not read the comments")
		return
	}
	if boards := h.commentBoards(r, caller, all); boards != nil {
		keep := map[string]bool{}
		for _, b := range boards {
			keep[b] = true
		}
		filtered := all[:0]
		for _, t := range all {
			if keep[t.Board] {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	}
	locked, _ := h.ss.ArtifactCommentsLocked(r.Context(), id, board)
	hubWrite := h.hubWriteOn(r)
	out := make([]commentView, 0, len(all))
	for _, t := range all {
		out = append(out, h.commentView(caller, t, locked, hubWrite))
	}
	canCreate, why := commentPolicy(caller, commentCreate, nil, locked, hubWrite)
	writeJSON(w, http.StatusOK, map[string]any{"comments": out, "locked": locked, "you": map[string]any{"kind": caller.Kind, "name": viewerName(caller), "can_comment": canCreate, "reason": why, "can_lock": caller.Kind == commentCallerOwner},
		"limits": map[string]int{"body": store.MaxArtifactCommentBody, "quote": maxCommentQuote, "comments": store.MaxArtifactComments}})
}

// readJSONBody reads a request body of at most max bytes into v; false means the request was answered.
func readJSONBody(w http.ResponseWriter, r *http.Request, max int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			errPayloadTooLarge(w, "that comment is too large")
			return false
		}
		errBadRequest(w, "the body must be JSON")
		return false
	}
	return true
}

// cleanAnchor validates an anchor and redacts the text it copies from the artifact, in place: sizes, valid UTF-8, sane lines, and the quote, prefix and suffix put through the same redactor as the
// body (an artifact can hold a token and a selection can cover it; the anchor is stored and served, and in phase 2 sent to an agent). msg is a refusal; unchecked is true when the redactor could not
// check the text, and the comment is then not kept.
func cleanAnchor(a *commentAnchor) (msg string, unchecked bool) {
	if a == nil {
		return "an anchor is required (the selected text, or whole: true)", false
	}
	if a.Whole {
		*a = commentAnchor{Whole: true}
	} else if strings.TrimSpace(a.Quote) == "" {
		return "the anchor needs the selected text or whole: true", false
	}
	if len(a.Quote) > maxCommentQuote || len(a.Prefix) > maxCommentContext || len(a.Suffix) > maxCommentContext {
		return "the selected text is too long to anchor a comment", false
	}
	for _, t := range []string{a.Quote, a.Prefix, a.Suffix} {
		if !utf8.ValidString(t) || strings.ContainsRune(t, 0) {
			return "the anchor is not valid text", false
		}
	}
	if a.StartLine < 0 || a.EndLine < 0 || a.StartLine > 10_000_000 || a.EndLine > 10_000_000 || (a.EndLine != 0 && a.EndLine < a.StartLine) {
		return "the line range is not valid", false
	}
	for _, f := range []*string{&a.Quote, &a.Prefix, &a.Suffix} {
		if *f == "" {
			continue
		}
		out, ok := redactComment(*f)
		if !ok {
			return "", true
		}
		*f = out
	}
	return "", false
}

// viewerName is how the viewer is addressed in the list's "you": the owner is "You", anyone else has no display name here.
func viewerName(c commentCaller) string {
	if c.Kind == commentCallerOwner {
		return commentYouLabel
	}
	return ""
}

// redactComment puts text through secrets.HubRedactor: the secret scanner plus the keyword rules the Coral Hub replies use (api_key=, "password": "...",
// PGPASSWORD=, Cookie:, --password VALUE: a value the scanner would keep). The keyword rules apply regardless of the Hub setting and of whether a hub is
// connected; only the owner's own switch, CORAL_REDACT_KEYWORDS=off in the environment, still turns them off, as everywhere else. A comment is at most 8 KB,
// so the cost is nothing, and a typed secret should not sit in sessions.db.
func redactComment(text string) (string, bool) {
	b, err := secrets.HubRedactor(func() bool { return true }).Text([]byte(text))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// commentCallerFor derives the caller and asks the policy whether this kind of caller may ever do the action, BEFORE the artifact is looked up: a caller who may not do it learns nothing about
// which artifact ids exist (404 vs 415 vs ok). The artifact, the comment, the lock and the author come after.
func (h *SessionsHandler) commentCallerFor(w http.ResponseWriter, r *http.Request, action string) (commentCaller, bool) {
	caller, status, code := h.artifactCommentCaller(r)
	if status != 0 {
		commentRefuse(w, status, code, "not allowed")
		return caller, false
	}
	if caller.Kind == commentCallerOwner && (action == commentCreate || action == commentResolve) {
		h.keepOwnerSubject(r.Context(), &caller) // the writes that stamp an identity make the owner's install id the first time
		if caller.Subject == "" {
			commentRefuse(w, http.StatusServiceUnavailable, "owner_identity_unavailable", "this computer could not make an identity for its owner, so the comment was not kept")
			return caller, false
		}
	}
	if ok, why := commentKindMay(caller, action, h.hubWriteOn(r)); !ok {
		commentRefuse(w, http.StatusForbidden, "comments_forbidden", why)
		return caller, false
	}
	return caller, true
}

// commentLocked reports whether comments on this artifact are read only: the artifact's lock, or the board's when the comment is made on a board. A comment made with NO board is not covered
// by a board lock, on purpose in phase 1: only the owner writes, and the owner holds the lock. When Coral Hub viewers may write, this must be revisited (a viewer could otherwise leave the board
// out to sidestep a board lock): refuse board-less writes while any board lock exists, or require the board.
func (h *SessionsHandler) commentLocked(r *http.Request, id, board string) bool {
	locked, _ := h.ss.ArtifactCommentsLocked(r.Context(), id, board)
	return locked
}

func (h *SessionsHandler) commentRate(w http.ResponseWriter, c commentCaller) bool {
	if c.Subject != "" && h.commentLimit.allow(c.Kind+"|"+c.Issuer+"|"+c.Subject, time.Now()) {
		return true
	}
	if c.Subject == "" {
		return true // no identity to key on: the policy refuses a hub write without one before this matters
	}
	// The marked 429 (X-Coral-Limit): a per-action limit, not an overload, so the dashboard's overload watch does not pause its pollers for it.
	httputil.WriteActionLimit(w, 60, "rate_limited", "too many comments; try again in a minute")
	return false
}

// POST /api/artifacts/{id}/comments {body, anchor, parent_id?, board?}
func (h *SessionsHandler) createArtifactComment(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.commentCallerFor(w, r, commentCreate)
	if !ok {
		return
	}
	id, ok := h.commentArtifact(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Body     string         `json:"body"`
		Anchor   *commentAnchor `json:"anchor"`
		ParentID *int64         `json:"parent_id"`
		Board    string         `json:"board"`
	}
	if !readJSONBody(w, r, 3*store.MaxArtifactCommentBody, &in) {
		return
	}
	if in.Board != "" && !commentBoardRe.MatchString(in.Board) {
		errBadRequest(w, "invalid board")
		return
	}
	locked := h.commentLocked(r, id, in.Board)
	hubWrite := h.hubWriteOn(r)
	if allowed, why := commentPolicy(caller, commentCreate, nil, locked, hubWrite); !allowed {
		commentRefuse(w, http.StatusForbidden, "comments_forbidden", why)
		return
	}
	if len(in.Body) > store.MaxArtifactCommentBody {
		errPayloadTooLarge(w, "a comment is at most "+strconv.Itoa(store.MaxArtifactCommentBody)+" bytes")
		return
	}
	anchorJSON := ""
	if in.ParentID == nil {
		msg, unchecked := cleanAnchor(in.Anchor)
		if msg != "" {
			errBadRequest(w, msg)
			return
		}
		if unchecked {
			commentRefuse(w, http.StatusServiceUnavailable, "redaction_unavailable", "the selected text could not be checked for secrets, so the comment was not kept")
			return
		}
		b, _ := json.Marshal(in.Anchor)
		anchorJSON = string(b)
	}
	body, ok := redactComment(in.Body)
	if !ok {
		commentRefuse(w, http.StatusServiceUnavailable, "redaction_unavailable", "the comment could not be checked for secrets, so it was not kept")
		return
	}
	if !h.commentRate(w, caller) {
		return
	}
	c, err := h.ss.CreateArtifactComment(r.Context(), store.ArtifactComment{ArtifactID: id, Board: in.Board, ParentID: in.ParentID, AnchorJSON: anchorJSON, Body: body,
		AuthorIssuer: caller.Issuer, AuthorSubject: caller.Subject, AuthorName: caller.Name, AuthorKind: caller.Kind})
	switch {
	case errors.Is(err, store.ErrCommentEmpty):
		errBadRequest(w, "a comment needs some text")
	case errors.Is(err, store.ErrCommentTooLong):
		errPayloadTooLarge(w, "that comment is too long")
	case errors.Is(err, store.ErrTooManyComments):
		commentRefuse(w, http.StatusConflict, "too_many_comments", "this artifact already has the most comments it can hold")
	case errors.Is(err, store.ErrBadParent):
		errBadRequest(w, "that comment cannot be replied to")
	case err != nil:
		slog.Warn("[artifact comments] create failed", "error", err)
		errInternalServer(w, "could not save the comment")
	default:
		writeJSON(w, http.StatusCreated, h.commentView(caller, c, locked, hubWrite))
	}
}

func commentID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	n, err := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	if err != nil || n <= 0 {
		errBadRequest(w, "invalid comment id")
		return 0, false
	}
	return n, true
}

// target loads the comment a route acts on and answers 404 when it is not there (or is a tombstone).
func (h *SessionsHandler) commentTarget(w http.ResponseWriter, r *http.Request, id string) (store.ArtifactComment, bool) {
	cid, ok := commentID(w, r)
	if !ok {
		return store.ArtifactComment{}, false
	}
	t, found, err := h.ss.GetArtifactComment(r.Context(), id, cid)
	if err != nil {
		errInternalServer(w, "could not read the comment")
		return t, false
	}
	if !found || t.DeletedAt != nil {
		errNotFound(w, "comment not found")
		return t, false
	}
	return t, true
}

// PATCH /api/artifacts/{id}/comments/{cid} {body}
func (h *SessionsHandler) editArtifactComment(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.commentCallerFor(w, r, commentEdit)
	if !ok {
		return
	}
	id, ok := h.commentArtifact(w, r, false)
	if !ok {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !readJSONBody(w, r, 3*store.MaxArtifactCommentBody, &in) {
		return
	}
	t, ok := h.commentTarget(w, r, id)
	if !ok {
		return
	}
	locked := h.commentLocked(r, id, t.Board)
	hubWrite := h.hubWriteOn(r)
	if allowed, why := commentPolicy(caller, commentEdit, &t, locked, hubWrite); !allowed {
		commentRefuse(w, http.StatusForbidden, "comments_forbidden", why)
		return
	}
	if len(in.Body) > store.MaxArtifactCommentBody {
		errPayloadTooLarge(w, "a comment is at most "+strconv.Itoa(store.MaxArtifactCommentBody)+" bytes")
		return
	}
	body, ok := redactComment(in.Body)
	if !ok {
		commentRefuse(w, http.StatusServiceUnavailable, "redaction_unavailable", "the comment could not be checked for secrets, so it was not kept")
		return
	}
	if !h.commentRate(w, caller) {
		return
	}
	c, found, err := h.ss.EditArtifactComment(r.Context(), id, t.ID, body)
	switch {
	case errors.Is(err, store.ErrCommentEmpty):
		errBadRequest(w, "a comment needs some text")
	case errors.Is(err, store.ErrCommentTooLong):
		errPayloadTooLarge(w, "that comment is too long")
	case err != nil:
		errInternalServer(w, "could not save the comment")
	case !found:
		errNotFound(w, "comment not found")
	default:
		writeJSON(w, http.StatusOK, h.commentView(caller, c, locked, hubWrite))
	}
}

// DELETE /api/artifacts/{id}/comments/{cid}: the author's own only.
func (h *SessionsHandler) deleteArtifactComment(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.commentCallerFor(w, r, commentDelete)
	if !ok {
		return
	}
	id, ok := h.commentArtifact(w, r, false)
	if !ok {
		return
	}
	t, ok := h.commentTarget(w, r, id)
	if !ok {
		return
	}
	locked := h.commentLocked(r, id, t.Board)
	if allowed, why := commentPolicy(caller, commentDelete, &t, locked, h.hubWriteOn(r)); !allowed {
		commentRefuse(w, http.StatusForbidden, "comments_forbidden", why)
		return
	}
	if !h.commentRate(w, caller) {
		return
	}
	if _, err := h.ss.DeleteArtifactComment(r.Context(), id, t.ID); err != nil {
		errInternalServer(w, "could not delete the comment")
		return
	}
	_ = h.ss.CloseArtifactAsks(r.Context(), t.ID, time.Now()) // a deleted comment is no thread an agent may still reply into
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// POST /api/artifacts/{id}/comments/{cid}/resolve and /reopen
func (h *SessionsHandler) resolveArtifactComment(resolved bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := h.commentCallerFor(w, r, commentResolve)
		if !ok {
			return
		}
		id, ok := h.commentArtifact(w, r, false)
		if !ok {
			return
		}
		t, ok := h.commentTarget(w, r, id)
		if !ok {
			return
		}
		locked := h.commentLocked(r, id, t.Board)
		hubWrite := h.hubWriteOn(r)
		if allowed, why := commentPolicy(caller, commentResolve, nil, locked, hubWrite); !allowed {
			commentRefuse(w, http.StatusForbidden, "comments_forbidden", why)
			return
		}
		if t.ParentID != nil {
			errBadRequest(w, "only a thread's first comment is resolved")
			return
		}
		if !h.commentRate(w, caller) {
			return
		}
		c, found, err := h.ss.SetArtifactCommentResolved(r.Context(), id, t.ID, resolved, caller.Issuer, caller.Subject)
		switch {
		case err != nil:
			errInternalServer(w, "could not save the change")
		case !found:
			errNotFound(w, "comment not found")
		default:
			if resolved {
				_ = h.ss.CloseArtifactAsks(r.Context(), t.ID, time.Now()) // a resolved thread takes no more agent replies
			}
			writeJSON(w, http.StatusOK, h.commentView(caller, c, locked, hubWrite))
		}
	}
}

// PUT /api/artifacts/{id}/comments/lock {scope: artifact|board, board?} and DELETE (the same body): the owner's switch.
func (h *SessionsHandler) lockArtifactComments(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.commentCallerFor(w, r, commentLock); !ok {
			return
		}
		id, ok := h.commentArtifact(w, r, false)
		if !ok {
			return
		}
		var in struct {
			Scope string `json:"scope"`
			Board string `json:"board"`
		}
		if r.ContentLength != 0 && !readJSONBody(w, r, 1<<10, &in) {
			return
		}
		scope, key := in.Scope, id
		switch scope {
		case "", "artifact":
			scope = "artifact"
		case "board":
			if !commentBoardRe.MatchString(in.Board) {
				errBadRequest(w, "a board lock needs the board")
				return
			}
			key = in.Board
		default:
			errBadRequest(w, "scope must be artifact or board")
			return
		}
		if err := h.ss.SetArtifactCommentsLocked(r.Context(), scope, key, on); err != nil {
			errInternalServer(w, "could not change the lock")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"locked": on, "scope": scope})
	}
}
