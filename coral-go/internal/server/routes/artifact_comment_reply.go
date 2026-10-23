package routes

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// An agent's reply into a thread it was asked into (#6357, #6455; the approved note, section 5). Agents cannot comment otherwise (commentKindMay): this route is the one way an agent writes, and
// the ask the owner made (artifact_comment_asks) is the gate. The check and the count are one step (SessionStore.ReserveAskReply): the ask is open and not expired, the agent is under its reply cap,
// and the reply is at most 8 KiB. The agent is the session its verified token names, never a name or id in the body; the reply is stored as a comment authored by that agent, so every reader sees it
// was an agent's. Local only: never through Coral Hub, and not in the tunnel table.

// POST /api/artifacts/{id}/comments/{cid}/reply {body}
func (h *SessionsHandler) agentReplyArtifactComment(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) || r.Header.Get(tunnel.ViaHeader) != "" {
		commentRefuse(w, http.StatusForbidden, "not_through_hub", "agent replies are not available through Coral Hub")
		return
	}
	caller, status, code := h.artifactCommentCaller(r)
	if status != 0 {
		commentRefuse(w, status, code, "not allowed")
		return
	}
	if caller.Kind != commentCallerAgent || caller.Subject == "" {
		commentRefuse(w, http.StatusForbidden, "agent_only", "only an agent asked into a thread replies here")
		return
	}
	id, ok := h.commentArtifact(w, r, false)
	if !ok {
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !readJSONBody(w, r, 2*store.MaxAskReplyBytes, &in) {
		return
	}
	t, ok := h.commentTarget(w, r, id)
	if !ok {
		return
	}
	if t.ParentID != nil {
		errBadRequest(w, "reply to the comment that starts the thread")
		return
	}
	// The agent must be the session that was asked: live, awake, on the comment's board, and the board not paused.
	ls, err := h.ss.GetLiveSession(r.Context(), caller.Subject)
	if err != nil || ls == nil || ls.IsSleeping == 1 || ls.IsArchived == 1 || t.Board == "" || ptr.Value(ls.BoardName) != t.Board {
		commentRefuse(w, http.StatusForbidden, "not_asked", "this agent was not asked into that thread")
		return
	}
	if h.boardHandler != nil && h.boardHandler.IsPaused(t.Board) {
		commentRefuse(w, http.StatusConflict, "board_paused", "the board is paused")
		return
	}
	if h.commentLocked(r, id, t.Board) {
		commentRefuse(w, http.StatusConflict, "comments_locked", "comments on this artifact are locked")
		return
	}
	if len(in.Body) > store.MaxAskReplyBytes {
		errPayloadTooLarge(w, "a reply is at most "+strconv.Itoa(store.MaxAskReplyBytes)+" bytes")
		return
	}
	body, ok := redactComment(in.Body)
	if !ok {
		commentRefuse(w, http.StatusServiceUnavailable, "redaction_unavailable", "the reply could not be checked for secrets, so it was not kept")
		return
	}
	if !h.commentRate(w, caller) {
		return
	}
	// The one atomic check, which also counts this reply.
	switch err := h.ss.ReserveAskReply(r.Context(), t.ID, caller.Subject, len(body), time.Now()); {
	case errors.Is(err, store.ErrAskNone):
		commentRefuse(w, http.StatusForbidden, "not_asked", "this agent was not asked into that thread")
		return
	case errors.Is(err, store.ErrAskClosed):
		commentRefuse(w, http.StatusConflict, "ask_ended", "the ask has ended")
		return
	case errors.Is(err, store.ErrAskReplyCap):
		commentRefuse(w, http.StatusConflict, "reply_limit", "this agent has left the most replies it may in that thread")
		return
	case errors.Is(err, store.ErrAskReplyTooBig):
		errPayloadTooLarge(w, "a reply is at most "+strconv.Itoa(store.MaxAskReplyBytes)+" bytes")
		return
	case err != nil:
		slog.Warn("[artifact comments] reply check failed", "error", err)
		errInternalServer(w, "could not check the ask")
		return
	}
	name := cleanCommentName(agentLabel(*ls))
	c, err := h.ss.CreateArtifactComment(r.Context(), store.ArtifactComment{ArtifactID: id, Board: t.Board, ParentID: &t.ID, Body: body,
		AuthorIssuer: caller.Issuer, AuthorSubject: caller.Subject, AuthorName: name, AuthorKind: commentCallerAgent})
	switch {
	case errors.Is(err, store.ErrCommentEmpty):
		h.ss.ReleaseAskReply(r.Context(), t.ID, caller.Subject)
		errBadRequest(w, "a reply needs some text")
	case errors.Is(err, store.ErrTooManyComments), errors.Is(err, store.ErrBadParent), errors.Is(err, store.ErrCommentTooLong):
		h.ss.ReleaseAskReply(r.Context(), t.ID, caller.Subject)
		commentRefuse(w, http.StatusConflict, "reply_not_kept", "the reply could not be kept")
	case err != nil:
		h.ss.ReleaseAskReply(r.Context(), t.ID, caller.Subject)
		slog.Warn("[artifact comments] agent reply failed", "error", err)
		errInternalServer(w, "could not save the reply")
	default:
		slog.Info("[artifact comments] agent replied into an asked thread", "artifact", id, "comment", t.ID, "agent", name, "board", t.Board)
		writeJSON(w, http.StatusCreated, h.commentView(caller, c, false, false))
	}
}
