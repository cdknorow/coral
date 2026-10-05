package routes

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
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
	// File.ReadDir returns directory order, so unlike os.ReadDir it does not
	// sort the whole listing a second time before ours.
	dirFile, err := os.Open(target)
	if err != nil {
		errNotFound(w, "Directory unavailable")
		return
	}
	listing, err := dirFile.ReadDir(-1)
	dirFile.Close()
	if err != nil {
		errNotFound(w, "Directory unavailable")
		return
	}
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	// Sort compact keys, lowercasing each name once, and build response paths
	// only for the page that is returned.
	type sortKey struct {
		name, folded string
		dir          bool
	}
	keys := make([]sortKey, len(listing))
	for i, item := range listing {
		name := item.Name()
		keys[i] = sortKey{name: name, folded: strings.ToLower(name), dir: item.IsDir()}
	}
	slices.SortFunc(keys, func(a, b sortKey) int {
		if a.dir != b.dir {
			if a.dir {
				return -1
			}
			return 1
		}
		if c := strings.Compare(a.folded, b.folded); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
	start := min(offset, len(keys))
	end := min(start+limit, len(keys))
	entries := make([]entry, 0, end-start)
	for _, k := range keys[start:end] {
		kind := "file"
		if k.dir {
			kind = "dir"
		}
		entries = append(entries, entry{k.name, filepath.ToSlash(filepath.Join(clean, k.name)), kind})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries, "dir": filepath.ToSlash(clean), "root": root,
		"offset": offset, "limit": limit, "has_more": end < len(keys),
	})
}
