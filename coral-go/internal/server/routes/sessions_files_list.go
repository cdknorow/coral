// A session's changed files: the list and its refresh (moved out of sessions_files.go, #5003; no behaviour change).

package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/subgentic/coral/internal/logsafe"
	"log/slog"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/store"
)

// filesCacheFloor is how long a changed-files list stays fresh when nothing
// refreshes it in the background: a diff mode the git poller does not
// compute, or polling turned off.
const filesCacheFloor = 30 * time.Second

// filesCacheMaxAge is how old a cached changed-files list may be before the
// Files handler recomputes it. The git poller rewrites only the branch_point
// list, every git_poll_interval_s, so that list stays fresh for two poll
// intervals; any other list was written by a refresh and is kept briefly.
func (h *SessionsHandler) filesCacheMaxAge(ctx context.Context, diffMode string) time.Duration {
	if diffMode != "branch_point" {
		return filesCacheFloor
	}
	settings, _ := h.ss.GetSettings(ctx)
	interval := background.GitPollInterval(settings, h.cfg.GitPollerIntervalS)
	if interval <= 0 {
		return filesCacheFloor
	}
	if interval < background.MinGitPollInterval {
		interval = background.MinGitPollInterval
	}
	return max(2*interval, filesCacheFloor)
}

// filesCacheFresh reports whether a cached list was written within maxAge.
// Every row of one list shares its recorded_at.
func filesCacheFresh(files []store.ChangedFile, maxAge time.Duration, now time.Time) bool {
	if len(files) == 0 {
		return false
	}
	recorded, err := time.Parse(store.ISOFormat, files[0].RecordedAt)
	if err != nil {
		return false
	}
	return now.Sub(recorded) <= maxAge
}

// Files returns changed files for a live agent.
// GET /api/sessions/live/{name}/files
// Returns the cached list for the user's current diff mode while it is
// fresh (see filesCacheMaxAge); otherwise computes it again, as a refresh
// does, so a list from before a rebase, merge or branch switch is never
// served as current.
func (h *SessionsHandler) Files(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sidPtr := querySessionID(r)

	diffMode := h.getDiffMode(r.Context())
	if diffMode == "" {
		diffMode = "branch_point"
	}

	files, found, err := h.gs.GetChangedFiles(r.Context(), name, sidPtr, diffMode)
	if err == nil && found && filesCacheFresh(files, h.filesCacheMaxAge(r.Context(), diffMode), time.Now()) {
		writeJSON(w, http.StatusOK, map[string]any{"agent_name": name, "files": hubVisibleFiles(r, files), "diff_mode": diffMode})
		return
	}

	sid := ""
	if sidPtr != nil {
		sid = *sidPtr
	}
	h.refreshFilesInner(w, r, name, sid)
}

// RefreshFiles runs fresh git queries and merges agent Write/Edit events.
// POST /api/sessions/live/{name}/files/refresh
func (h *SessionsHandler) RefreshFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		SessionID string `json:"session_id"`
	}
	decodeJSON(r, &body)
	h.refreshFilesInner(w, r, name, body.SessionID)
}

// trackedPaths reports which of the given repo-relative paths git tracks. It
// asks only about those paths, so the cost follows the number of candidates
// rather than the size of the repository. An empty list costs nothing.
func trackedPaths(ctx context.Context, workdir string, paths []string) map[string]bool {
	tracked := make(map[string]bool, len(paths))
	if len(paths) == 0 {
		return tracked
	}

	args := append([]string{"ls-files", "-z", "--"}, paths...)
	out, err := gitCmd(ctx, workdir, args...).Output()
	if err != nil {
		// Unknown tracked status: treat every candidate as untracked, which at
		// worst shows a clean file in the list rather than hiding a dirty one.
		slog.Warn("git ls-files (tracked) failed", "workdir", logsafe.Dir(workdir), "error", err)
		return tracked
	}
	for _, f := range bytes.Split(out, []byte{0}) {
		if len(f) > 0 {
			tracked[string(f)] = true
		}
	}
	return tracked
}

// gitCmd builds a git command for the changed-files path. --no-optional-locks
// keeps a read-only query from refreshing and rewriting the index, which on a
// large repo means disk writes and lock contention on every poll.
func gitCmd(ctx context.Context, workdir string, args ...string) *exec.Cmd {
	return gitutil.Command(ctx, workdir, args...)
}

// refreshFilesInner contains the shared logic for computing fresh changed files.
func (h *SessionsHandler) refreshFilesInner(w http.ResponseWriter, r *http.Request, name, sessionID string) {
	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Could not determine working directory", "files": []any{}})
		return
	}

	// New files' line counts are read through the working tree held as a root (#3596 W5): a
	// symlinked folder on the way, a symlink, a FIFO or a device is never read. If the tree can't
	// be opened, new files count 0 lines.
	root, rootErr := safefs.OpenRoot(workdir)
	if rootErr == nil {
		defer root.Close()
	}
	lineCount := func(rel string) int {
		if rootErr != nil {
			return 0
		}
		return gitutil.NewFileLineCountIn(root, rel)
	}

	// Run git diff --numstat to get changed files
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	diffMode := h.getDiffMode(r.Context())
	base := gitutil.GetDiffBase(ctx, workdir, diffMode)
	out, err := gitCmd(ctx, workdir, "diff", base, "--numstat").Output()
	fileMap := make(map[string]store.ChangedFile)
	if err != nil {
		slog.Warn("git diff --numstat failed", "agent_name", name, "workdir", logsafe.Dir(workdir), "error", err)
	} else {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) < 3 {
				continue
			}
			adds, _ := strconv.Atoi(parts[0])
			dels, _ := strconv.Atoi(parts[1])
			fileMap[parts[2]] = store.ChangedFile{Filepath: parts[2], Additions: adds, Deletions: dels, Status: "M"}
		}
	}

	// Include untracked files
	untrackedSet := make(map[string]bool)
	untrackedOut, err := gitCmd(ctx, workdir, "ls-files", "--others", "--exclude-standard").Output()
	if err != nil {
		slog.Warn("git ls-files (untracked) failed", "agent_name", name, "workdir", logsafe.Dir(workdir), "error", err)
	} else {
		counted := 0
		for _, f := range strings.Split(strings.TrimSpace(string(untrackedOut)), "\n") {
			if f == "" {
				continue
			}
			untrackedSet[f] = true
			if _, exists := fileMap[f]; !exists {
				adds := 0
				if counted < gitutil.MaxNewFileStatFiles {
					adds = lineCount(f)
					counted++
				}
				fileMap[f] = store.ChangedFile{Filepath: f, Additions: adds, Deletions: 0, Status: "??"}
			}
		}
	}

	// Files the agent wrote or edited that git has not already reported.
	// Collect the candidates first so their tracked status can be asked for by
	// pathspec: this used to list every tracked file in the repo to answer a
	// question about a handful of paths, which on a monorepo is the single
	// most expensive thing here.
	sidPtr := ptr.NonEmpty(sessionID)
	events, _ := h.ts.ListAgentEvents(r.Context(), name, 200, sidPtr)
	candidates := make([]string, 0, 16)
	seen := make(map[string]bool)
	for _, ev := range events {
		if ev.ToolName == nil || (*ev.ToolName != "Write" && *ev.ToolName != "Edit") {
			continue
		}
		if ev.DetailJSON == nil {
			continue
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(*ev.DetailJSON), &detail); err != nil {
			continue
		}
		fp, ok := detail["file_path"].(string)
		if !ok || fp == "" {
			continue
		}
		rel, err := filepath.Rel(workdir, fp)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if _, exists := fileMap[rel]; exists || seen[rel] {
			continue
		}
		seen[rel] = true
		candidates = append(candidates, rel)
	}

	trackedSet := trackedPaths(ctx, workdir, candidates)

	for _, rel := range candidates {
		// Skip files that no longer exist on disk, or aren't a regular file inside the tree (an
		// event's file_path comes from a hook payload; looked up through the root, #3596 W5)
		if rootErr != nil {
			continue
		}
		info, statErr := root.Lstat(rel)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		// Skip tracked files not in fileMap — they have no diff (clean)
		if trackedSet[rel] && !untrackedSet[rel] {
			continue
		}
		fileMap[rel] = store.ChangedFile{
			Filepath:  rel,
			Additions: lineCount(rel),
			Deletions: 0,
			Status:    "??",
		}
	}

	// Annotate files with agent attribution from agent_events
	if sessionID != "" {
		if edits, err := h.ts.GetFileEdits(r.Context(), sessionID, workdir); err == nil {
			for fp, agents := range edits {
				if f, exists := fileMap[fp]; exists {
					f.Agents = agents
					f.Source = "git"
					fileMap[fp] = f
				}
			}
		}
	}

	// Set source for files without explicit attribution
	for fp, f := range fileMap {
		if f.Source == "" {
			f.Source = "git"
			fileMap[fp] = f
		}
	}

	// Update DB cache
	files := make([]store.ChangedFile, 0, len(fileMap))
	for _, f := range fileMap {
		files = append(files, f)
	}
	if diffMode == "" {
		diffMode = "branch_point"
	}
	h.gs.ReplaceChangedFiles(r.Context(), name, workdir, files, sidPtr, diffMode)

	writeJSON(w, http.StatusOK, map[string]any{"agent_name": name, "files": hubVisibleFiles(r, files), "diff_mode": diffMode})
}
