package routes

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/executil"
)

// Swapped in tests so nothing is launched.
var (
	installedEditors = executil.InstalledEditors
	openInEditor     = executil.OpenInEditor
)

// ListEditors returns the desktop editors installed on this machine.
// GET /api/system/editors
func (h *SessionsHandler) ListEditors(w http.ResponseWriter, r *http.Request) {
	editors := []executil.Editor{}
	// Editors open on the machine running Coral, so they are only offered to a browser on that
	// same machine (#3585: a proxied request, or any request with the setting on, is not local).
	if auth.IsDirectLocal(r) {
		editors = append(editors, installedEditors()...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"editors": editors})
}

// OpenInEditor opens a file reference from an agent's chat in a desktop
// editor, at its :line when it has one. The reference is resolved like
// resolve-path, so only files inside the agent's git root open.
// POST /api/sessions/live/{name}/open-in-editor {filepath, session_id, editor}
func (h *SessionsHandler) OpenInEditor(w http.ResponseWriter, r *http.Request) {
	if !auth.IsDirectLocal(r) {
		errForbidden(w, "Editors can only be opened from this computer")
		return
	}
	var body struct {
		Filepath  string `json:"filepath"`
		SessionID string `json:"session_id"`
		Editor    string `json:"editor"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "Invalid JSON")
		return
	}
	ref := strings.TrimSpace(body.Filepath)
	if ref == "" || strings.ContainsAny(ref, "\x00") || body.Editor == "" {
		errBadRequest(w, "filepath and editor are required")
		return
	}
	f, err := h.resolveFileRef(r.Context(), chi.URLParam(r, "name"), body.SessionID, ref, strictBounds(r))
	if err != nil {
		writeResolveErr(w, err)
		return
	}
	if err := openInEditor(body.Editor, f.abs, f.line); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
