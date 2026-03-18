package routes

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
)

// SystemHandler handles settings, tags, and filesystem endpoints.
type SystemHandler struct {
	db  *store.DB
	cfg *config.Config
}

// NewSystemHandler creates a SystemHandler.
func NewSystemHandler(db *store.DB, cfg *config.Config) *SystemHandler {
	return &SystemHandler{db: db, cfg: cfg}
}

// ── Settings ────────────────────────────────────────────────────────────

// GetSettings returns all user settings.
// GET /api/settings
func (h *SystemHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryxContext(r.Context(), "SELECT key, value FROM user_settings")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			settings[k] = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

// PutSettings upserts one or more settings.
// PUT /api/settings
func (h *SystemHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	for k, v := range body {
		_, err := h.db.ExecContext(r.Context(),
			"INSERT INTO user_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
			k, v)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Status returns server status.
// GET /api/system/status
func (h *SystemHandler) Status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"startup_complete": true,
		"version":          "0.1.0-go",
	})
}

// RefreshIndexer triggers a manual re-index.
// POST /api/indexer/refresh
func (h *SystemHandler) RefreshIndexer(w http.ResponseWriter, r *http.Request) {
	// TODO: trigger indexer
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ListFilesystem lists directories for the directory browser.
// GET /api/filesystem/list?path=~
func (h *SystemHandler) ListFilesystem(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		reqPath = "~"
	}

	// Expand ~ to home directory
	if strings.HasPrefix(reqPath, "~") {
		home, _ := os.UserHomeDir()
		reqPath = filepath.Join(home, reqPath[1:])
	}

	expanded, err := filepath.Abs(reqPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}

	// Security: restrict to home directory
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(expanded, home) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access denied"})
		return
	}

	entries, err := os.ReadDir(expanded)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"path":    expanded,
			"entries": []any{},
			"error":   err.Error(),
		})
		return
	}

	type dirEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
	}

	dirs := make([]dirEntry, 0)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // skip hidden
		}
		if e.IsDir() {
			dirs = append(dirs, dirEntry{Name: e.Name(), IsDir: true})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })

	writeJSON(w, http.StatusOK, map[string]any{
		"path":    expanded,
		"entries": dirs,
	})
}

// ── Tags ────────────────────────────────────────────────────────────────

// ListTags returns all tags.
// GET /api/tags
func (h *SystemHandler) ListTags(w http.ResponseWriter, r *http.Request) {
	type tag struct {
		ID    int    `json:"id" db:"id"`
		Name  string `json:"name" db:"name"`
		Color string `json:"color" db:"color"`
	}
	var tags []tag
	if err := h.db.SelectContext(r.Context(), &tags, "SELECT id, name, color FROM tags ORDER BY name"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if tags == nil {
		tags = []tag{}
	}
	writeJSON(w, http.StatusOK, tags)
}

// CreateTag creates a new tag.
// POST /api/tags
func (h *SystemHandler) CreateTag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if body.Color == "" {
		body.Color = "#58a6ff"
	}
	result, err := h.db.ExecContext(r.Context(),
		"INSERT INTO tags (name, color) VALUES (?, ?)", body.Name, body.Color)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "tag already exists"})
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
// POST /api/sessions/{sessionID}/tags
func (h *SystemHandler) AddSessionTag(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	var body struct {
		TagID int `json:"tag_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TagID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tag_id is required"})
		return
	}
	h.db.ExecContext(r.Context(),
		"INSERT OR IGNORE INTO session_tags (session_id, tag_id) VALUES (?, ?)",
		sessionID, body.TagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveSessionTag removes a tag from a session.
// DELETE /api/sessions/{sessionID}/tags/{tagID}
func (h *SystemHandler) RemoveSessionTag(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	tagID, _ := strconv.Atoi(chi.URLParam(r, "tagID"))
	h.db.ExecContext(r.Context(),
		"DELETE FROM session_tags WHERE session_id = ? AND tag_id = ?",
		sessionID, tagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
