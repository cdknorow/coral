// Reading, previewing and saving a file's content (moved out of sessions_files.go, #5003; no behaviour change).

package routes

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/safefs"
)

// GetFileContent returns the raw content of a file in the agent's working tree.
// GET /api/sessions/live/{name}/file-content?filepath=...&session_id=...
func (h *SessionsHandler) GetFileContent(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	fp := r.URL.Query().Get("filepath")
	sessionID := r.URL.Query().Get("session_id")

	if fp == "" || strings.HasPrefix(fp, "-") || strings.ContainsAny(fp, "\x00") {
		errBadRequest(w, "filepath is required")
		return
	}

	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		errBadRequest(w, "Could not determine working directory")
		return
	}

	strict := strictBounds(r)
	fullPath, err := filepath.Abs(filepath.Join(workdir, fp))
	tempFile := false
	if filepath.IsAbs(fp) {
		if strict {
			// Through the tunnel an absolute path is only honoured inside the session's
			// own repository; anything else looks exactly like a missing file.
			real := realPath(fp)
			if !withinAny(h.sessionFileRoots(r, name, sessionID), real) {
				errNotFound(w, "File not found")
				return
			}
			fullPath = real
		} else if real, rerr := safefs.Resolve(fp); rerr == nil {
			// Local dashboard: absolute paths are read-only, wherever they are.
			fullPath, err, tempFile = real, nil, true
		}
	}
	if err != nil {
		errBadRequest(w, "invalid path")
		return
	}
	if strict && !rootAllowed(realPath(workdir)) {
		errNotFound(w, "File not found")
		return
	}
	if !tempFile && !isPathWithinDir(workdir, fullPath) {
		if strict {
			errNotFound(w, "File not found")
		} else {
			errForbidden(w, "Path traversal not allowed")
		}
		return
	}
	if sensitivePath(realOrSelf(fullPath)) {
		errNotFound(w, "File not found")
		return
	}

	// Read through a folder held open (#3672, path audit #3669 finding 2), as SaveFileContent
	// writes: the open is non-blocking (a FIFO or socket in the work tree can't hang the
	// request), the file must be a regular file on the open handle, a symlink swapped in after
	// the checks above isn't followed, and under strict bounds (a tunnelled or proxied request)
	// a file with another name (a hard link to a file outside the tree) is refused. The local
	// dashboard may read a linked file: trees hold legitimate hard links (pnpm's node_modules,
	// cp -l copies, build caches), and its reads are already allowed outside the tree with the
	// denylist as the guard (Security's SHOULD on #3672). The working tree is the root; a local
	// dashboard's absolute read outside it opens the file's own folder.
	rootDir, rel := workdir, ""
	if tempFile {
		rootDir, rel = filepath.Dir(fullPath), filepath.Base(fullPath)
	}
	root, err := safefs.OpenRoot(rootDir)
	if err != nil {
		errNotFound(w, "File not found")
		return
	}
	defer root.Close()
	if rel == "" {
		if rel, err = safefs.Within(root.Dir(), fullPath); err != nil || rel == "." {
			errNotFound(w, "File not found")
			return
		}
	}
	read := root.ReadFile
	if strict {
		read = root.ReadFileOneLink
	}
	content, err := read(rel, maxFileBytes())
	if errors.Is(err, safefs.ErrTooLarge) {
		errPayloadTooLarge(w, "File too large to preview")
		return
	}
	if err != nil {
		// Missing, a folder, not a regular file, or linked: all look like a missing file.
		errNotFound(w, "File not found")
		return
	}
	if r.URL.Query().Get("raw") == "1" {
		writeRawFilePreview(w, fp, content)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"filepath":          fp,
		"content":           string(content),
		"working_directory": workdir,
	})
}

// Image types the file preview can display, by extension.
var previewImageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".svg":  "image/svg+xml",
}

var previewMediaTypes = map[string]string{
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg",
	".m4a": "audio/mp4", ".aac": "audio/aac", ".flac": "audio/flac",
	".mp4": "video/mp4", ".webm": "video/webm", ".ogv": "video/ogg", ".mov": "video/quicktime", ".m4v": "video/mp4",
}

// writeRawImage serves an image's bytes for the preview's <img> tags (the
// ?raw=1 form of file-content and file-original). Only image types are
// served, and an SVG opened directly cannot run script on this origin.
func writeRawImage(w http.ResponseWriter, fp string, data []byte) {
	ct, ok := previewImageTypes[strings.ToLower(filepath.Ext(fp))]
	if !ok {
		errBadRequest(w, "raw content is only served for images")
		return
	}
	writeRawPreview(w, ct, data, true)
}

func writeRawPreview(w http.ResponseWriter, ct string, data []byte, image bool) {
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	if image {
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	} else {
		// Audio/video are inert media resources; allow the native player to
		// fetch the bytes without applying a document sandbox.
		h.Set("Content-Security-Policy", "default-src 'none'; media-src 'self'")
	}
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func writeRawMedia(w http.ResponseWriter, fp string, data []byte) {
	ct, ok := previewMediaTypes[strings.ToLower(filepath.Ext(fp))]
	if !ok {
		errBadRequest(w, "raw content is only served for previewable media")
		return
	}
	writeRawPreview(w, ct, data, false)
}

func writeRawFilePreview(w http.ResponseWriter, fp string, data []byte) {
	ext := strings.ToLower(filepath.Ext(fp))
	if _, ok := previewImageTypes[ext]; ok {
		writeRawImage(w, fp, data)
		return
	}
	if _, ok := previewMediaTypes[ext]; ok {
		writeRawMedia(w, fp, data)
		return
	}
	errBadRequest(w, "raw content is only served for previewable media")
}

// GetFileOriginal returns the original (git base) content of a file.
// GET /api/sessions/live/{name}/file-original?filepath=...&session_id=...
func (h *SessionsHandler) GetFileOriginal(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	fp := r.URL.Query().Get("filepath")
	sessionID := r.URL.Query().Get("session_id")

	if fp == "" || strings.HasPrefix(fp, "-") || strings.ContainsAny(fp, "\x00:") {
		errBadRequest(w, "filepath is required")
		return
	}

	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		errBadRequest(w, "Could not determine working directory")
		return
	}

	// Path traversal protection
	fullPath, err := filepath.Abs(filepath.Join(workdir, fp))
	if err != nil {
		errBadRequest(w, "invalid path")
		return
	}
	if !isPathWithinDir(workdir, fullPath) {
		errForbidden(w, "path traversal not allowed")
		return
	}
	// The same bounds as Diff for anything but this computer's own dashboard (#3596 W2): the
	// file at the diff base can hold what the working tree no longer shows (a committed .env).
	if strictBounds(r) && diffPathRefused(workdir, fp, fullPath) {
		errNotFound(w, "File not found")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	base := gitutil.GetDiffBase(ctx, workdir, h.getDiffMode(r.Context()))

	// git show <ref>:<path> needs paths relative to the repo root, not the workdir.
	prefix := gitutil.ShowPrefix(ctx, workdir)

	raw := r.URL.Query().Get("raw") == "1"
	out, err := gitutil.Command(ctx, workdir, "show", base+":"+prefix+fp).Output()
	if err != nil {
		if raw {
			errNotFound(w, "File is new since the diff base")
			return
		}
		// File doesn't exist in the base commit (new file)
		writeJSON(w, http.StatusOK, map[string]any{
			"filepath":          fp,
			"content":           "",
			"working_directory": workdir,
		})
		return
	}
	if raw {
		writeRawFilePreview(w, fp, out)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"filepath":          fp,
		"content":           string(out),
		"working_directory": workdir,
	})
}

// SaveFileContent writes content to a file in the agent's working tree.
// PUT /api/sessions/live/{name}/file-content?filepath=...&session_id=...
func (h *SessionsHandler) SaveFileContent(w http.ResponseWriter, r *http.Request) {
	// Limit body size to 50MB
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)

	name := chi.URLParam(r, "name")
	fp := r.URL.Query().Get("filepath")
	sessionID := r.URL.Query().Get("session_id")

	if fp == "" {
		errBadRequest(w, "filepath is required")
		return
	}

	var body struct {
		Content *string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Content == nil {
		errBadRequest(w, "content is required")
		return
	}

	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		errBadRequest(w, "Could not determine working directory")
		return
	}

	// Through safefs (#3596, W1): the working tree is held open as a root, which must still
	// exist (a gone one used to mean "anything goes"); the path is decided on its resolved
	// form, so a symlinked ancestor can't lead out; and the write goes through the root, so
	// nothing swapped in after the check can either.
	root, err := safefs.OpenRoot(workdir)
	if err != nil {
		errBadRequest(w, "the working directory no longer exists")
		return
	}
	defer root.Close()
	target := fp
	if !filepath.IsAbs(target) {
		target = filepath.Join(root.Dir(), target)
	}
	rel, err := safefs.Within(root.Dir(), target)
	if err != nil || rel == "." {
		errForbidden(w, "Path traversal not allowed")
		return
	}

	// Through the tunnel nothing that git runs may be written (hooks, its config, a .git
	// file that points git elsewhere, .husky, a core.hooksPath folder), nor any
	// credential-bearing file. Decided on the resolved target, case-insensitively.
	if strictBounds(r) {
		resolved := filepath.Join(root.Dir(), rel)
		if !rootAllowed(root.Dir()) || gitControlPath(rel) || sensitivePath(resolved) || h.inHooksPath(r.Context(), root.Dir(), rel) {
			errForbidden(w, "Path not allowed")
			return
		}
	}

	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		errInternalServer(w, "could not write file")
		return
	}
	// Decided again on the path the opened file really has, so a folder swapped for a link into
	// .git after the check above can't carry the write there (Security's SHOULD 2).
	var deny func(string) bool
	if strictBounds(r) {
		deny = func(fresh string) bool {
			return gitControlPath(fresh) || sensitivePath(filepath.Join(root.Dir(), fresh)) || h.inHooksPath(r.Context(), root.Dir(), fresh)
		}
	}
	if err := root.WriteFileUnless(rel, []byte(*body.Content), 0o644, deny); err != nil {
		if errors.Is(err, safefs.ErrNotRegular) || errors.Is(err, safefs.ErrLinked) || errors.Is(err, safefs.ErrDenied) || errors.Is(err, safefs.ErrOutside) {
			errForbidden(w, "Path not allowed")
			return
		}
		errInternalServer(w, "could not write file")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "filepath": fp})
}

// diffPathRefused applies, for anything but this computer's own dashboard (Coral Hub, an API
// key, a remote client), the same bounds as file-content to a file a diff or its base shows
// (#3558, Security's #3556 finding 1; #3596 W2): a root that isn't the filesystem or the home
// folder, no sensitive path, no dot-file or dot-folder (.env.local, .git/config), inside the
// working tree once resolved, and one file, not a folder's worth (QA's #3558 review). Refused
// files look exactly like missing ones.
func diffPathRefused(workdir, fp, fullPath string) bool {
	real := realOrSelf(fullPath)
	if !rootAllowed(realPath(workdir)) || diffPathHidden(fp) || sensitivePath(real) ||
		!isPathWithinDir(realOrSelf(workdir), real) {
		return true
	}
	if rel, err := filepath.Rel(realOrSelf(workdir), real); err != nil || diffPathHidden(rel) {
		return true
	}
	if fi, err := os.Stat(real); err == nil && fi.IsDir() {
		return true
	}
	return false
}
