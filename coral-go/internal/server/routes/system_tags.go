// Session and folder tags (moved out of system.go, #5003; no behaviour change).

package routes

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/store"
)

// ListTags returns all tags.
// GET /api/tags
func (h *SystemHandler) ListTags(w http.ResponseWriter, r *http.Request) {
	type tag struct {
		ID    int    `json:"id" db:"id"`
		Name  string `json:"name" db:"name"`
		Color string `json:"color" db:"color"`
	}
	var tags []tag
	if err := h.db.Reader().SelectContext(r.Context(), &tags, "SELECT id, name, color FROM tags ORDER BY name"); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tags))
}

// CreateTag creates a new tag.
// POST /api/tags
func (h *SystemHandler) CreateTag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" {
		errBadRequest(w, "Tag name is required")
		return
	}
	if body.Color == "" {
		body.Color = "#58a6ff"
	}
	if !store.ValidTagColor(body.Color) {
		// The dashboard puts a tag's colour into a style attribute: "red;position:fixed;inset:0" would restyle the page (#4875).
		errBadRequest(w, "color must be #rgb, #rrggbb or #rrggbbaa")
		return
	}
	result, err := h.db.ExecContext(r.Context(),
		"INSERT INTO tags (name, color) VALUES (?, ?)", body.Name, body.Color)
	if err != nil {
		errConflict(w, "tag already exists")
		return
	}
	id, _ := result.LastInsertId()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": body.Name, "color": body.Color})
}

// DeleteTag removes a tag.
// DELETE /api/tags/{tagID}
func (h *SystemHandler) DeleteTag(w http.ResponseWriter, r *http.Request) {
	tagID, _ := strconv.Atoi(chi.URLParam(r, "tagID"))
	h.db.ExecContext(r.Context(), "DELETE FROM tags WHERE id = ?", tagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AddSessionTag adds a tag to a session.
// POST /api/sessions/history/{sessionID}/tags
func (h *SystemHandler) AddSessionTag(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	var body struct {
		TagID int `json:"tag_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.TagID == 0 {
		errBadRequest(w, "tag_id is required")
		return
	}
	h.db.ExecContext(r.Context(),
		"INSERT OR IGNORE INTO session_tags (session_id, tag_id) VALUES (?, ?)",
		sessionID, body.TagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveSessionTag removes a tag from a session.
// DELETE /api/sessions/history/{sessionID}/tags/{tagID}
func (h *SystemHandler) RemoveSessionTag(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	tagID, _ := strconv.Atoi(chi.URLParam(r, "tagID"))
	h.db.ExecContext(r.Context(),
		"DELETE FROM session_tags WHERE session_id = ? AND tag_id = ?",
		sessionID, tagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GetAllFolderTags returns all folder tags grouped by folder name.
// GET /api/folder-tags
func (h *SystemHandler) GetAllFolderTags(w http.ResponseWriter, r *http.Request) {
	result, err := h.ss.GetAllFolderTags(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if result == nil {
		result = map[string][]store.Tag{}
	}
	writeJSON(w, http.StatusOK, result)
}

// GetFolderTags returns tags for a specific folder.
// GET /api/folder-tags/{folderName}
func (h *SystemHandler) GetFolderTags(w http.ResponseWriter, r *http.Request) {
	folderName := chi.URLParam(r, "folderName")
	tags, err := h.ss.GetFolderTags(r.Context(), folderName)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tags))
}

// AddFolderTag adds a tag to a folder.
// POST /api/folder-tags/{folderName}
func (h *SystemHandler) AddFolderTag(w http.ResponseWriter, r *http.Request) {
	folderName := chi.URLParam(r, "folderName")
	var body struct {
		TagID int64 `json:"tag_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.TagID == 0 {
		errBadRequest(w, "tag_id is required")
		return
	}
	if err := h.ss.AddFolderTag(r.Context(), folderName, body.TagID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveFolderTag removes a tag from a folder.
// DELETE /api/folder-tags/{folderName}/{tagID}
func (h *SystemHandler) RemoveFolderTag(w http.ResponseWriter, r *http.Request) {
	folderName := chi.URLParam(r, "folderName")
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tagID"), 10, 64)
	if err := h.ss.RemoveFolderTag(r.Context(), folderName, tagID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
