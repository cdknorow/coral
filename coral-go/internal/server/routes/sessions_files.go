package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/ptr"
	"github.com/cdknorow/coral/internal/safefs"
	"github.com/cdknorow/coral/internal/store"
)

// resolveWorkdir determines the working directory for an agent session.
func (h *SessionsHandler) resolveWorkdir(ctx context.Context, name, agentType, sessionID string) string {
	pane, _ := h.terminal.FindSession(ctx, name, agentType, sessionID)
	if pane != nil {
		if pane.CurrentPath != "" {
			return pane.CurrentPath
		}
	}
	if sessionID != "" {
		if live, err := h.ss.GetLiveSession(ctx, sessionID); err == nil && live != nil && live.WorkingDir != "" {
			return live.WorkingDir
		}
		snap, err := h.gs.GetLatestGitStateBySession(ctx, sessionID)
		if err == nil && snap != nil {
			return snap.WorkingDirectory
		}
	}
	snap, err := h.gs.GetLatestGitState(ctx, name)
	if err == nil && snap != nil {
		return snap.WorkingDirectory
	}
	return ""
}

// resolveGitRoot returns the git toplevel for the agent's working directory.
func (h *SessionsHandler) resolveGitRoot(ctx context.Context, name, agentType, sessionID string) string {
	workdir := h.resolveWorkdir(ctx, name, agentType, sessionID)
	if workdir == "" {
		return ""
	}
	return trustedGitRoot(ctx, workdir)
}

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
	json.NewDecoder(r.Body).Decode(&body)
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
		slog.Warn("git ls-files (tracked) failed", "workdir", workdir, "error", err)
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
		slog.Warn("git diff --numstat failed", "agent_name", name, "workdir", workdir, "error", err)
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
		slog.Warn("git ls-files (untracked) failed", "agent_name", name, "workdir", workdir, "error", err)
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

// Diff returns the unified diff for a single file.
// GET /api/sessions/live/{name}/diff
func (h *SessionsHandler) Diff(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	fp := r.URL.Query().Get("filepath")
	sessionID := r.URL.Query().Get("session_id")

	if fp == "" || strings.HasPrefix(fp, "-") || strings.ContainsAny(fp, "\x00") {
		errBadRequest(w, "invalid filepath")
		return
	}

	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		errBadRequest(w, "Could not determine working directory")
		return
	}

	strict := strictBounds(r)

	// Path traversal protection
	fullPath, _ := filepath.Abs(filepath.Join(workdir, fp))
	if !isPathWithinDir(workdir, fullPath) {
		if strict {
			errNotFound(w, "File not found")
		} else {
			errForbidden(w, "path traversal not allowed")
		}
		return
	}

	// Anything but this computer's own dashboard (Coral Hub, an API key, a remote client)
	// gets the same bounds as file-content (#3558, Security's #3556 finding 1): a root that
	// isn't the filesystem or the home folder, no sensitive path, no dot-file or dot-folder
	// (.env.local, .git/config), a size cap, a filtered patch and no local path. Refused
	// files look exactly like missing ones.
	if strict && diffPathRefused(workdir, fp, fullPath) {
		errNotFound(w, "File not found")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	base := gitutil.GetDiffBase(ctx, workdir, h.getDiffMode(r.Context()))
	gitArgs := []string{"diff", base, "--", fp}
	if strict {
		// The filepath is a name, never a pathspec: '*' or ':(glob)**' would diff the whole
		// tree (QA's #3558 review).
		gitArgs = append([]string{"--literal-pathspecs"}, gitArgs...)
	}
	out, err := gitutil.Command(ctx, workdir, gitArgs...).Output()
	diffText := ""
	if err != nil {
		slog.Warn("git diff failed", "agent_name", name, "filepath", fp, "error", err)
	} else {
		diffText = string(out)
	}

	// For untracked files, show the file content as a "new file" diff
	if diffText == "" {
		fullPath := filepath.Join(workdir, fp)
		realFull, err := safefs.Resolve(fullPath)
		realWork, _ := safefs.Resolve(workdir)
		if err == nil && strings.HasPrefix(realFull, realWork+string(os.PathSeparator)) {
			if info, err := os.Stat(realFull); err == nil && !info.IsDir() {
				if strict && (info.Size() > maxFileBytes() || isBinaryFile(realFull)) {
					diffText = string(diffNote("new file %q, %d bytes, not shown", fp, info.Size()))
				} else if data, err := os.ReadFile(realFull); err == nil {
					lines := strings.Split(string(data), "\n")
					diffText = fmt.Sprintf("diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n",
						fp, fp, fp, len(lines))
					for _, line := range lines {
						diffText += "+" + line + "\n"
					}
				}
			}
		}
	}

	if strict {
		diffText = string(capDiff(filterPatch([]byte(diffText)), int(maxFileBytes())))
		writeJSON(w, http.StatusOK, map[string]any{"filepath": fp, "diff": diffText})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"filepath": fp, "diff": diffText, "working_directory": workdir})
}

// ChangesDiff returns a downloadable unified diff for all changes in an
// agent's current working directory, including committed changes since the
// configured diff base, working-tree changes, and untracked files.
// GET /api/sessions/live/{name}/changes.diff
func (h *SessionsHandler) ChangesDiff(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sessionID := r.URL.Query().Get("session_id")
	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		errBadRequest(w, "Could not determine working directory")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Anything but this computer's own dashboard (Coral Hub, a Bearer-key proxy, curl): filtered
	// and capped, no local path (#3555), and only for a working tree that isn't the home folder
	// or the filesystem root (#3596 W3).
	tunneled := strictBounds(r)
	if tunneled && !rootAllowed(realPath(workdir)) {
		errNotFound(w, "Could not generate changes.diff")
		return
	}
	gen := h.generateChangesDiff
	if tunneled {
		gen = h.generatePersistedDiff
	}
	diff, err := gen(ctx, workdir)
	if err != nil {
		slog.Warn("git changes diff failed", "agent_name", name, "workdir", workdir, "error", err)
		errInternalServer(w, "Could not generate changes.diff")
		return
	}

	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="changes.diff"`)
	if !tunneled {
		w.Header().Set("X-Coral-Working-Directory", workdir)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(diff)
}

func (h *SessionsHandler) generateChangesDiff(ctx context.Context, workdir string) ([]byte, error) {
	base := gitutil.GetDiffBase(ctx, workdir, h.getDiffMode(ctx))
	diff, err := gitutil.Command(ctx, workdir, "diff", "--binary", base, "--").Output()
	if err != nil {
		return nil, err
	}

	// A normal git diff omits untracked files. Render each as a new-file patch.
	untracked, err := gitutil.Command(ctx, workdir, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, err
	}
	for _, rawPath := range bytes.Split(untracked, []byte{0}) {
		if len(rawPath) == 0 {
			continue
		}
		cmd := gitutil.Command(ctx, workdir, "diff", "--no-index", "--binary", "--", os.DevNull, string(rawPath))
		fileDiff, fileErr := cmd.Output()
		// git diff --no-index returns 1 when differences were found.
		if fileErr != nil {
			if exitErr, ok := fileErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
				return nil, fmt.Errorf("diff untracked file %q: %w", string(rawPath), fileErr)
			}
		}
		if len(diff) > 0 && diff[len(diff)-1] != '\n' {
			diff = append(diff, '\n')
		}
		diff = append(diff, fileDiff...)
	}
	return diff, nil
}

// SessionChanges returns a session's unified diff using only its stable ID.
// Live sessions are generated from their current working directory; stopped
// sessions fall back to the artifact captured when they were terminated.
// GET /api/sessions/{sessionID}/changes
func (h *SessionsHandler) SessionChanges(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if !safefs.IsSafeID(sessionID) { // one path element: never ".", ".." or a separator (#3596 W4)
		errBadRequest(w, "invalid session_id")
		return
	}

	strict := strictBounds(r)
	workdir := h.resolveGitRoot(r.Context(), "", "", sessionID)
	if workdir != "" && (!strict || rootAllowed(realPath(workdir))) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		gen := h.generateChangesDiff
		if strict {
			gen = h.generatePersistedDiff // not this computer's dashboard: filtered and capped (#3555, #3596 W3)
		}
		if diff, err := gen(ctx, workdir); err == nil {
			writeSessionChanges(w, r, diff, workdir)
			return
		} else {
			slog.Warn("git session changes failed", "session_id", sessionID, "workdir", workdir, "error", err)
		}
	}

	data, err := os.ReadFile(h.sessionChangesArtifactPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			errNotFound(w, "session changes not found")
			return
		}
		errInternalServer(w, err.Error())
		return
	}
	if strict {
		data = safeDiff(data) // a stored diff (also one from before #3555), filtered (#3596 W3)
	}
	writeSessionChanges(w, r, data, "")
}

func writeSessionChanges(w http.ResponseWriter, r *http.Request, diff []byte, workdir string) {
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="changes.diff"`)
	if workdir != "" && !strictBounds(r) { // a local path only for this computer's dashboard (#3555, #3596 W3)
		w.Header().Set("X-Coral-Working-Directory", workdir)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(diff)
}

// PersistTaskChanges stores a board task's final patch before its working
// directory can be cleaned up.
func (h *SessionsHandler) PersistTaskChanges(ctx context.Context, task *board.Task, artifactPath string) error {
	if task == nil || task.SessionID == nil || *task.SessionID == "" {
		return fmt.Errorf("task has no session_id")
	}
	workdir := h.resolveGitRoot(ctx, "", "", *task.SessionID)
	if workdir == "" {
		return fmt.Errorf("could not determine working directory")
	}
	diff, err := h.generatePersistedDiff(ctx, workdir) // filtered and capped (#3555)
	if err != nil {
		return err
	}
	return writeChangesArtifact(artifactPath, diff)
}

func writeChangesArtifact(artifactPath string, diff []byte) error {
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		return err
	}
	// Identical diffs are stored once (#3555): the content goes to
	// artifacts/diff-objects/<sha256> and the task's or session's changes.diff is a hard link
	// to it, so the same unchanged checkout doesn't take its full size again for every task,
	// and removing one task's diff never touches another's.
	if obj, err := storeDiffObject(artifactPath, diff); err == nil {
		tmp := artifactPath + ".link-tmp"
		os.Remove(tmp)
		if os.Link(obj, tmp) == nil {
			if err := os.Rename(tmp, artifactPath); err == nil {
				return nil
			}
			os.Remove(tmp)
		}
	}
	// No hard links here: a plain copy.
	tmp, err := os.CreateTemp(filepath.Dir(artifactPath), ".changes-*.diff")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(diff); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, artifactPath)
}

// storeDiffObject stores diff once under <coral>/artifacts/diff-objects/<sha256> (found from
// artifactPath: .../artifacts/{tasks,sessions}/<id>/changes.diff) and returns its path.
func storeDiffObject(artifactPath string, diff []byte) (string, error) {
	root := filepath.Dir(filepath.Dir(filepath.Dir(artifactPath))) // .../artifacts
	if filepath.Base(root) != "artifacts" {
		return "", fmt.Errorf("not under artifacts/")
	}
	sum := sha256.Sum256(diff)
	dir := filepath.Join(root, "diff-objects")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	obj := filepath.Join(dir, hex.EncodeToString(sum[:]))
	if _, err := os.Stat(obj); err == nil {
		return obj, nil
	}
	if err := writeFileAtomic(obj, diff, 0o600); err != nil {
		return "", err
	}
	return obj, nil
}

func (h *SessionsHandler) sessionChangesArtifactPath(sessionID string) string {
	return filepath.Join(h.cfg.CoralDir(), "artifacts", "sessions", sessionID, "changes.diff")
}

func (h *SessionsHandler) persistSessionChanges(ctx context.Context, sessionID, workdir string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("session_id required")
	}
	if workdir == "" {
		workdir = h.resolveGitRoot(ctx, "", "", sessionID)
	} else {
		workdir = gitutil.ResolveGitRoot(ctx, workdir)
	}
	if workdir == "" {
		return "", fmt.Errorf("could not determine working directory")
	}
	if !gitutil.IsWorkTree(ctx, workdir) {
		return "", errNoGitCheckout
	}
	diff, err := h.generatePersistedDiff(ctx, workdir)
	if err != nil {
		return "", err
	}
	path := h.sessionChangesArtifactPath(sessionID)
	if err := writeChangesArtifact(path, diff); err != nil {
		return "", err
	}
	return path, nil
}

// errNoGitCheckout: the session's folder isn't a git checkout, so there are no changes
// to snapshot. Not a failure: the kill answers with no artifact and no error (#3520).
var errNoGitCheckout = errors.New("not a git checkout")

// changesArtifactProblem is what the kill answer (and the dashboard's toast) says when
// the snapshot failed: a plain sentence, never git's raw error, which goes to the log
// (#3520, QA's bug bash F6: "exit status 129").
const changesArtifactProblem = "git couldn't produce the diff (details in coral.log)"

// SessionChangesArtifact serves a patch captured before a session was killed.
// GET /api/sessions/{sessionID}/changes.diff
func (h *SessionsHandler) SessionChangesArtifact(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if !safefs.IsSafeID(sessionID) { // one path element: never ".", ".." or a separator (#3596 W4)
		errBadRequest(w, "invalid session_id")
		return
	}
	data, err := os.ReadFile(h.sessionChangesArtifactPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			errNotFound(w, "session changes artifact not found")
			return
		}
		errInternalServer(w, err.Error())
		return
	}
	if strictBounds(r) {
		data = safeDiff(data) // not this computer's dashboard: filtered and capped, old diffs too (#3555, #3596 W3)
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="changes.diff"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// SearchFiles searches for files in the agent's working directory.
// GET /api/sessions/live/{name}/search-files
//
// Query parameters:
//
//	q          - search query (fuzzy mode)
//	dir        - directory path to list (directory browsing mode)
//	session_id - optional session identifier
//
// When 'dir' is provided, returns entries in that directory with type info
// (directory browsing mode). When 'q' is provided, returns fuzzy matches
// (search mode). When neither is provided, returns the first 50 files.
func (h *SessionsHandler) SearchFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	query := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("q")))
	dir := r.URL.Query().Get("dir")
	sessionID := r.URL.Query().Get("session_id")
	if r.URL.Query().Get("view") == "explorer" {
		serveExplorerDirectory(w, r, h.resolveGitRoot(r.Context(), name, "", sessionID), dir, strictBounds(r))
		return
	}

	// Read file search limit from user settings (default 500)
	fileSearchLimit := 500
	if settings, err := h.ss.GetSettings(r.Context()); err == nil {
		if v, ok := settings["file_search_limit"]; ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				fileSearchLimit = n
			}
		}
	}

	workdir := h.resolveGitRoot(r.Context(), name, "", sessionID)
	if workdir == "" {
		writeJSON(w, http.StatusOK, map[string]any{"files": []string{}})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Directory browsing mode: list entries in a specific directory
	if dir != "" {
		h.searchFilesDir(w, ctx, workdir, dir, query, fileSearchLimit, strictBounds(r))
		return
	}

	// The list is cached per repository (#3721): it is shared, so filtered into a new slice.
	allFiles, err := fileLists.get(ctx, workdir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"files": []string{}})
		return
	}
	if strictBounds(r) {
		if !rootAllowed(realPath(workdir)) {
			allFiles = nil
		}
		kept := make([]string, 0, len(allFiles))
		for _, fp := range allFiles {
			if !sensitivePath(fp) {
				kept = append(kept, fp)
			}
		}
		allFiles = kept
	}

	if query == "" {
		if len(allFiles) > fileSearchLimit {
			allFiles = allFiles[:fileSearchLimit]
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": allFiles})
		return
	}

	// Score matches: basename match > basename contains > path contains
	type scored struct {
		score int
		path  string
	}
	var matches []scored
	for _, fp := range allFiles {
		fpLower := strings.ToLower(fp)
		basename := strings.ToLower(filepath.Base(fp))
		if strings.Contains(fpLower, query) {
			s := 2
			if basename == query {
				s = 0
			} else if strings.Contains(basename, query) {
				s = 1
			}
			matches = append(matches, scored{s, fp})
		}
	}
	// Sort by score then path
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score < matches[j].score
		}
		return matches[i].path < matches[j].path
	})
	result := make([]string, 0, fileSearchLimit)
	for i, m := range matches {
		if i >= fileSearchLimit {
			break
		}
		result = append(result, m.path)
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": result})
}

// searchFilesDir lists filesystem entries in a specific directory for
// directory browsing mode. Uses os.ReadDir for direct filesystem access,
// like ls/tab-completion. Returns entries with name, path, and type
// (dir/file). Directories are listed first, then files. An optional
// filter (from the 'q' parameter) restricts results to matching names.
func (h *SessionsHandler) searchFilesDir(w http.ResponseWriter, _ context.Context, workdir, dir, filter string, limit int, strict bool) {
	// Normalize dir: strip leading/trailing slashes, use "." for root
	dir = strings.TrimPrefix(dir, "/")
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	}

	// Prevent path traversal
	if strings.Contains(dir, "..") {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}})
		return
	}

	// Build the absolute path to read
	absDir := workdir
	if dir != "." {
		absDir = filepath.Join(workdir, dir)
	}

	// A symlink inside the workspace must not lead the listing out of it.
	realWD, realDir := realPath(workdir), realPath(absDir)
	if !within(realWD, realDir) || (strict && (!rootAllowed(realWD) || sensitivePath(realDir))) {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "dir": dir})
		return
	}
	dirEntries, err := os.ReadDir(realDir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "dir": dir})
		return
	}

	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"` // "dir" or "file"
	}

	var dirs []entry
	var files []entry
	showHidden := strings.HasPrefix(filter, ".")

	for _, de := range dirEntries {
		name := de.Name()
		if strict && !listingVisible(realDir, name) {
			continue
		}

		// Skip hidden files/dirs unless filter starts with .
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}

		// Skip non-text files and files over 1MB
		if !de.IsDir() {
			if !isTextFile(name) {
				continue
			}
			if info, err := de.Info(); err == nil && info.Size() > 1<<20 {
				continue
			}
		}

		// Apply filter if provided
		if filter != "" && !strings.Contains(strings.ToLower(name), filter) {
			continue
		}

		entryPath := name
		if dir != "." {
			entryPath = dir + "/" + name
		}

		if de.IsDir() {
			dirs = append(dirs, entry{
				Name: name + "/",
				Path: entryPath,
				Type: "dir",
			})
		} else {
			files = append(files, entry{
				Name: name,
				Path: entryPath,
				Type: "file",
			})
		}
	}

	// Sort dirs and files alphabetically by name (case-insensitive)
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	sort.Slice(files, func(i, j int) bool { return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name) })

	// Combine: directories first, then files
	entries := make([]entry, 0, len(dirs)+len(files))
	entries = append(entries, dirs...)
	entries = append(entries, files...)

	if len(entries) > limit {
		entries = entries[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"dir":     dir,
	})
}

// textExtensions is a whitelist of file extensions known to be text/source
// files safe to preview in the web UI. Files not matching this list are
// excluded from directory browsing results to prevent loading binary files.
var textExtensions = map[string]bool{
	// Go
	".go": true, ".mod": true, ".sum": true,
	// JavaScript / TypeScript
	".js": true, ".ts": true, ".tsx": true, ".jsx": true, ".mjs": true, ".cjs": true,
	// Web
	".html": true, ".htm": true, ".css": true, ".scss": true, ".less": true,
	// Data / Config
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".xml": true,
	".csv": true, ".ini": true, ".cfg": true, ".conf": true, ".properties": true,
	// Documentation
	".md": true, ".txt": true, ".rst": true, ".adoc": true,
	// Python
	".py": true, ".pyi": true, ".pyx": true,
	// Ruby
	".rb": true, ".erb": true, ".rake": true, ".gemspec": true,
	// Rust
	".rs": true,
	// C / C++
	".c": true, ".h": true, ".cpp": true, ".hpp": true, ".cc": true, ".hh": true,
	// Java / JVM
	".java": true, ".kt": true, ".kts": true, ".scala": true, ".gradle": true,
	// Shell
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	// SQL / Query
	".sql": true, ".graphql": true, ".gql": true,
	// Protocol / Schema
	".proto": true, ".avro": true, ".thrift": true,
	// Environment / Config files
	".env": true, ".envrc": true,
	".gitignore": true, ".gitattributes": true, ".gitmodules": true,
	".dockerignore": true, ".editorconfig": true,
	".eslintrc": true, ".prettierrc": true, ".babelrc": true,
	// Swift / Objective-C
	".swift": true, ".m": true, ".mm": true,
	// Other
	".log": true, ".lock": true, ".patch": true, ".diff": true,
	".tf": true, ".hcl": true, // Terraform
	".lua": true, ".vim": true, ".el": true,
	".r": true, ".R": true, ".jl": true, // R, Julia
	".ex": true, ".exs": true, // Elixir
	".hs": true, ".cabal": true, // Haskell
	".pl": true, ".pm": true, // Perl
	".php": true, ".twig": true,
	".dart": true, ".svelte": true, ".vue": true,
	".nix": true, ".dhall": true,
	".tmpl": true, ".tpl": true, // Go templates
	".plist": true, // macOS property lists
}

// textFileNames are known text files without extensions.
var textFileNames = map[string]bool{
	"Makefile": true, "Dockerfile": true, "Containerfile": true,
	"LICENSE": true, "README": true, "CHANGELOG": true,
	"Gemfile": true, "Rakefile": true, "Procfile": true,
	"Vagrantfile": true, "Brewfile": true,
	"CLAUDE.md": true, "MEMORY.md": true,
	".gitignore": true, ".gitattributes": true, ".gitmodules": true,
	".dockerignore": true, ".editorconfig": true,
	".eslintrc": true, ".prettierrc": true,
}

// isTextFile returns true if the filename has a known text file extension
// or is a known extensionless text file.
func isTextFile(name string) bool {
	if textFileNames[name] {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false
	}
	return textExtensions[ext]
}

// Git returns recent git snapshots for a live agent.
// GET /api/sessions/live/{name}/git
func (h *SessionsHandler) Git(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	sessionID := r.URL.Query().Get("session_id")
	limit := clampLimit(queryInt(r, "limit", 20), 20, 100)

	var snapshots []store.GitSnapshot
	var err error
	if sessionID != "" {
		snapshots, err = h.gs.GetGitSnapshotsForSession(r.Context(), sessionID, limit)
	} else {
		snapshots, err = h.gs.GetGitSnapshots(r.Context(), name, limit)
	}
	if err != nil || snapshots == nil {
		snapshots = []store.GitSnapshot{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_name": name, "snapshots": snapshots})
}

// ResolvePath turns a file reference from an agent's chat (repo-relative,
// relative to the agent's folder, or absolute, with an optional :line or
// :line-line suffix) into the git-root-relative path the file endpoints use.
// Only existing files inside the agent's git root resolve.
// GET /api/sessions/live/{name}/resolve-path?filepath=...&session_id=...
func (h *SessionsHandler) ResolvePath(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("filepath"))
	if ref == "" || strings.ContainsAny(ref, "\x00") {
		errBadRequest(w, "filepath is required")
		return
	}
	f, err := h.resolveFileRef(r.Context(), chi.URLParam(r, "name"), r.URL.Query().Get("session_id"), ref, strictBounds(r))
	if err != nil {
		writeResolveErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"filepath": f.rel, "line": f.line})
}

type resolvedFileRef struct {
	rel  string // relative to the git root, slash-separated
	abs  string // absolute, symlinks resolved
	line int
}

var (
	errNoWorkdir    = errors.New("Could not determine working directory")
	errFileNotFound = errors.New("File not found")
)

func writeResolveErr(w http.ResponseWriter, err error) {
	if err == errNoWorkdir {
		errBadRequest(w, err.Error())
	} else {
		errNotFound(w, err.Error())
	}
}

func (h *SessionsHandler) resolveFileRef(ctx context.Context, name, sessionID, ref string, strict bool) (resolvedFileRef, error) {
	fp, line := strings.TrimSpace(ref), 0
	if unescaped, err := url.PathUnescape(fp); err == nil {
		fp = unescaped
	}
	if strings.HasPrefix(strings.ToLower(fp), "file://localhost/") {
		fp = fp[len("file://localhost"):]
	}
	fp = strings.TrimPrefix(fp, "file://")

	if m := fileRefHashLineRe.FindStringSubmatch(fp); m != nil {
		fp = m[1]
		line, _ = strconv.Atoi(m[2])
	} else if m := fileRefLineRe.FindStringSubmatch(fp); m != nil {
		fp = m[1]
		line, _ = strconv.Atoi(m[2])
	} else if idx := strings.Index(fp, "#"); idx != -1 {
		fp = fp[:idx]
	}

	// ~/path is the host user's home directory
	if fp == "~" || strings.HasPrefix(fp, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			fp = filepath.Join(home, strings.TrimPrefix(fp, "~"))
		}
	}

	root := h.resolveGitRoot(ctx, name, "", sessionID)
	if root == "" {
		root = h.resolveWorkdir(ctx, name, "", sessionID)
	}
	if root == "" {
		return resolvedFileRef{}, errNoWorkdir
	}
	realRoot, err := safefs.Resolve(root)
	if err != nil {
		return resolvedFileRef{}, errNoWorkdir
	}
	if strict && !rootAllowed(realRoot) {
		return resolvedFileRef{}, errFileNotFound // same answer as any missing file
	}

	check := func(c string) (resolvedFileRef, bool) {
		real, err := safefs.Resolve(c)
		if err != nil {
			return resolvedFileRef{}, false
		}
		if info, err := os.Stat(real); err != nil || info.IsDir() {
			return resolvedFileRef{}, false
		}
		if !strings.HasPrefix(real, realRoot+string(os.PathSeparator)) {
			// Local dashboard: absolute paths open read-only wherever they are. Through
			// the tunnel nothing outside the repository resolves, and the answer is the
			// same as for a missing file (no existence oracle).
			if filepath.IsAbs(fp) && !strict {
				return resolvedFileRef{rel: real, abs: real, line: line}, true
			}
			return resolvedFileRef{}, false
		}
		if sensitivePath(real) {
			return resolvedFileRef{}, false
		}
		rel, err := filepath.Rel(realRoot, real)
		if err != nil {
			return resolvedFileRef{}, false
		}
		return resolvedFileRef{rel: filepath.ToSlash(rel), abs: real, line: line}, true
	}

	wd := h.resolveWorkdir(ctx, name, "", sessionID)
	var candidates []string
	if filepath.IsAbs(fp) {
		candidates = []string{fp}
	} else {
		candidates = []string{filepath.Join(root, fp)}
		if wd != "" && wd != root {
			candidates = append(candidates, filepath.Join(wd, fp))
		}
	}
	for _, c := range candidates {
		if f, ok := check(c); ok {
			return f, nil
		}
	}

	// Agents often shorten paths (static/app.js for a file several folders
	// down), so fall back to repo files whose path ends with the reference.
	for _, rel := range suffixMatches(ctx, root, wd, fp, h.changedFileSet(ctx, name, sessionID)) {
		if f, ok := check(filepath.Join(root, rel)); ok {
			return f, nil
		}
	}
	return resolvedFileRef{}, errFileNotFound
}

// changedFileSet is the agent's changed files (git-root relative) from the
// last Files refresh.
func (h *SessionsHandler) changedFileSet(ctx context.Context, name, sessionID string) map[string]bool {
	var sid *string
	if sessionID != "" {
		sid = &sessionID
	}
	diffMode := h.getDiffMode(ctx)
	if diffMode == "" {
		diffMode = "branch_point"
	}
	files, _, _ := h.gs.GetChangedFiles(ctx, name, sid, diffMode)
	set := make(map[string]bool, len(files))
	for _, f := range files {
		set[f.Filepath] = true
	}
	return set
}

// suffixMatches lists the repo files (tracked, or untracked and not ignored)
// whose path ends with ref on a folder boundary, best guess first: files the
// agent has changed, then files under its working directory, then the
// shallowest.
func suffixMatches(ctx context.Context, root, workdir, ref string, changed map[string]bool) []string {
	ref = strings.TrimPrefix(filepath.ToSlash(ref), "./")
	if ref == "" || strings.HasPrefix(ref, "../") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "~") ||
		strings.ContainsAny(ref, "*?[]\\:") {
		return nil
	}
	out, err := gitCmd(ctx, root, "ls-files", "-z", "-c", "-o", "--exclude-standard", "--", "*/"+ref).Output()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var matches []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && strings.HasSuffix(p, "/"+ref) && !seen[p] {
			seen[p] = true
			matches = append(matches, p)
		}
	}

	wdPrefix := ""
	realRoot, err1 := safefs.Resolve(root)
	realWd, err2 := safefs.Resolve(workdir)
	if rel, err := filepath.Rel(realRoot, realWd); err1 == nil && err2 == nil && err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		wdPrefix = filepath.ToSlash(rel) + "/"
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if ci, cj := changed[matches[i]], changed[matches[j]]; ci != cj {
			return ci
		}
		ii, ij := wdPrefix != "" && strings.HasPrefix(matches[i], wdPrefix), wdPrefix != "" && strings.HasPrefix(matches[j], wdPrefix)
		if ii != ij {
			return ii
		}
		di, dj := strings.Count(matches[i], "/"), strings.Count(matches[j], "/")
		if di != dj {
			return di < dj
		}
		return matches[i] < matches[j]
	})
	return matches
}

// A trailing :line, :line:col, :line-line, or #Lline hash on a file reference.
var (
	fileRefLineRe     = regexp.MustCompile(`^(.+?):(\d+)(?:[-:]\d+)?$`)
	fileRefHashLineRe = regexp.MustCompile(`^(.+?)#(?:L)?(\d+)(?:[-:]L?\d+)?$`)
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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
