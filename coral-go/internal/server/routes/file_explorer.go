package routes

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Explorer lists names only, one directory at a time. Preview limits are
// enforced separately when the user opens a file.
func serveExplorerDirectory(w http.ResponseWriter, r *http.Request, root, dir string) {
	const limit = 500
	offset, ok := parseBoundedInt(r.URL.Query().Get("offset"), 0, 0, 1000000)
	if !ok {
		errBadRequest(w, "invalid explorer offset")
		return
	}
	if root == "" {
		errNotFound(w, "Working directory unavailable")
		return
	}
	clean := filepath.Clean(dir)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		errBadRequest(w, "directory must be inside the workspace")
		return
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		errNotFound(w, "Working directory unavailable")
		return
	}
	target, err := filepath.EvalSymlinks(filepath.Join(realRoot, clean))
	if err != nil {
		errNotFound(w, "Directory unavailable")
		return
	}
	rel, err := filepath.Rel(realRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		errBadRequest(w, "directory must be inside the workspace")
		return
	}
	listing, err := os.ReadDir(target)
	if err != nil {
		errNotFound(w, "Directory unavailable")
		return
	}
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	entries := make([]entry, 0, len(listing))
	for _, item := range listing {
		kind := "file"
		if item.IsDir() {
			kind = "dir"
		}
		entries = append(entries, entry{item.Name(), filepath.ToSlash(filepath.Join(clean, item.Name())), kind})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "dir"
		}
		a, b := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if a == b {
			return entries[i].Name < entries[j].Name
		}
		return a < b
	})
	start := min(offset, len(entries))
	end := min(start+limit, len(entries))
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries[start:end], "dir": filepath.ToSlash(clean), "root": root,
		"offset": offset, "limit": limit, "has_more": end < len(entries),
	})
}
