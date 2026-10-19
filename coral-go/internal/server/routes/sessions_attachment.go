package routes

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/jsonl"
)

// ChatAttachment serves one pasted image of a session's chat (#5032). GET /api/sessions/live/{name}/attachment/{ref}
//
// A user turn that carried an image has it in the transcript itself (Claude: an image content block; Codex: a data URL), and the chat's chip names it by
// a reference (agent.ImageRef: 32 hex characters, the hash of the image's base64 text), never by the bytes or a path. This finds the image in the session's
// own transcript again by that hash, decodes it under a size cap and sniffs it: PNG, JPEG, GIF or WebP only (415 otherwise, SVG included, whatever the
// transcript's own media type says). The reply has GET /api/upload/{name}'s hardening. The session is resolved as the chat resolves it, so a request reaches
// what the chat route does for the same caller (the Operator, or an agent token on its own session under the identity rules); a reference that matches no
// image in that transcript is 404, and one that is not a reference is 400.
func (h *SessionsHandler) ChatAttachment(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "ref")
	if !agent.ValidImageRef(ref) {
		errBadRequest(w, "not an attachment reference")
		return
	}
	q := r.URL.Query()
	t := h.resolveChatTarget(r, chi.URLParam(r, "name"), q.Get("agent_type"), q.Get("session_id"), q.Get("working_directory"))
	id := t.sessionID
	if id == "" {
		id = t.name
	}
	path := jsonl.TranscriptPath(id, t.workingDir, t.agentType)
	if path == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no transcript for that session"})
		return
	}
	data, err := jsonl.FindImage(r.Context(), path, t.agentType, ref)
	switch {
	case err == nil:
	case errors.Is(err, jsonl.ErrImageNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such attachment"})
		return
	case errors.Is(err, jsonl.ErrImageTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "the attachment is too large to show"})
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	default:
		errInternalServer(w, "Failed to read the attachment")
		return
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	ct := http.DetectContentType(head)
	if !thumbnailTypes[ct] {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "not a PNG, JPEG, GIF or WebP image"})
		return
	}
	setImageHeaders(w, ct, "attachment-"+ref, int64(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
