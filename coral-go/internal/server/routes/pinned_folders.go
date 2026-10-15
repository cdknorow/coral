package routes

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cdknorow/coral/internal/safefs"
	"github.com/cdknorow/coral/internal/store"
)

// Pinned folders (#3630): the Operator pins a folder to the Agents list, where it stays as a
// folder entry with no agents in it, ready to launch into, until it is unpinned. Pinning and
// unpinning are the Operator's (a person: this computer's dashboard, the signed command line,
// or Coral Hub, whose tunnel allows them only at Launch); a pinned folder passes the same check
// as a launch's working directory.

const maxPinnedPathLen = 4096

// pinnedFolderJSON is a pinned folder as the dashboard shows it.
type pinnedFolderJSON struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
}

func pinnedFolderView(path string) pinnedFolderJSON {
	return pinnedFolderJSON{Path: path, Name: filepath.Base(path), Exists: isDir(path)}
}

// pinnedPath is raw as a clean absolute path, or the reason it can't be one. need: the folder
// must exist (pinning); unpinning accepts a folder that is gone.
func pinnedPath(raw string, need bool) (string, string) {
	p := strings.TrimSpace(raw)
	switch {
	case p == "":
		return "", "path is required"
	case len(p) > maxPinnedPathLen:
		return "", "path is too long"
	case strings.IndexFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return "", "path has characters a folder name can't have"
	}
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "can't find the home folder for ~"
		}
		p = filepath.Join(home, p[1:])
	}
	if safefs.IsUNC(p) {
		return "", "a network (UNC) path can't be pinned"
	}
	if !filepath.IsAbs(p) {
		return "", "use the folder's full path"
	}
	p = filepath.Clean(p)
	if need {
		if msg := launchDirError(p); msg != "" {
			return "", msg
		}
	}
	return p, ""
}

func refuseUnlessPerson(w http.ResponseWriter, r *http.Request, what string) bool {
	if humanInputCaller(r) {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the Operator can " + what, "code": "operator_only"})
	return true
}

// ListPinnedFolders: GET /api/pinned-folders.
func (h *SystemHandler) ListPinnedFolders(w http.ResponseWriter, r *http.Request) {
	pins, err := h.ss.ListPinnedFolders(r.Context())
	if err != nil {
		errInternalServer(w, "could not read the pinned folders")
		return
	}
	out := make([]pinnedFolderJSON, 0, len(pins))
	for _, p := range pins {
		out = append(out, pinnedFolderView(p.Path))
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": out})
}

// PinFolder: POST /api/pinned-folders {path}. The Operator only.
func (h *SystemHandler) PinFolder(w http.ResponseWriter, r *http.Request) {
	if refuseUnlessPerson(w, r, "pin a folder") {
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	p, msg := pinnedPath(body.Path, true)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg, "field": "path"})
		return
	}
	if err := h.ss.PinFolder(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrTooManyPinnedFolders) {
			errConflict(w, "at most 100 folders can be pinned; unpin one first")
			return
		}
		errInternalServer(w, "could not pin the folder")
		return
	}
	writeJSON(w, http.StatusOK, pinnedFolderView(p))
}

// UnpinFolder: DELETE /api/pinned-folders {path}. The Operator only.
func (h *SystemHandler) UnpinFolder(w http.ResponseWriter, r *http.Request) {
	if refuseUnlessPerson(w, r, "unpin a folder") {
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	p, msg := pinnedPath(body.Path, false)
	if msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg, "field": "path"})
		return
	}
	removed, err := h.ss.UnpinFolder(r.Context(), p)
	if err != nil {
		errInternalServer(w, "could not unpin the folder")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "unpinned": removed})
}
