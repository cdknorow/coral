// Browsing the server's folders (moved out of system.go, #5003; no behaviour change).

package routes

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/safefs"
)

// expandHome expands a leading ~ to the home directory.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}

// browsable resolves a requested directory. Through the tunnel it must lie inside the home
// directory or a configured project root after symlink resolution and must not be a
// credential location; otherwise it is reported exactly like a missing directory.
func browsable(r *http.Request, reqPath string) (string, bool) {
	// A network path is reported like a missing folder, before anything stats it (#3671): on
	// Windows the stat alone connects to the host as the user. Every caller (the folder browser,
	// is-git) stats what this returns.
	if safefs.RefuseRemote(reqPath) != nil {
		return "", false
	}
	expanded, err := filepath.Abs(expandHome(reqPath))
	if err != nil || safefs.RefuseRemote(expanded) != nil {
		return "", false
	}
	if !strictBounds(r) {
		return expanded, true
	}
	real := realPath(expanded)
	if real == "" || !withinAny(projectRoots(), real) || sensitivePath(real) {
		return "", false
	}
	return real, true
}

// ListFilesystem lists directories for the directory browser.
// GET /api/filesystem/list?path=~
func (h *SystemHandler) ListFilesystem(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		reqPath = "~"
	}
	expanded, ok := browsable(r, reqPath)
	if !ok {
		errForbidden(w, "access denied")
		return
	}

	// Verify the path exists and is a directory (no traversal beyond real paths)
	info, err := os.Stat(expanded)
	if err != nil || !info.IsDir() {
		errForbidden(w, "access denied")
		return
	}

	entries, err := os.ReadDir(expanded)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"path":    expanded,
			"entries": []any{},
			"error":   "cannot read directory",
		})
		return
	}

	// Python returns flat string array of directory names (not objects)
	dirs := make([]string, 0)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // skip hidden
		}
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)

	writeJSON(w, http.StatusOK, map[string]any{
		"path":    expanded,
		"entries": dirs,
	})
}

// IsGitDir checks if a directory is a git repository.
// GET /api/filesystem/is-git?path=/some/dir
func (h *SystemHandler) IsGitDir(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"is_git": false})
		return
	}
	expanded, ok := browsable(r, reqPath)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]bool{"is_git": false})
		return
	}
	_, err := os.Stat(filepath.Join(expanded, ".git"))
	resp := map[string]any{"is_git": err == nil}
	if err == nil {
		// The repository's current branch ("" when it has no commit yet): the launch and job forms prefill Base Branch with it instead of "main" (#6701).
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		resp["branch"] = gitutil.CurrentBranch(ctx, expanded)
	}
	writeJSON(w, http.StatusOK, resp)
}
