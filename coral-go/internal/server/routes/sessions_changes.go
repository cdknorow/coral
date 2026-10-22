// A session's diffs and its changes artifact (moved out of sessions_files.go, #5003; no behaviour change).

package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/safefs"
)

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
		slog.Warn("git diff failed", "agent_name", name, "filepath", logsafe.Dir(fp), "error", err)
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
		slog.Warn("git changes diff failed", "agent_name", name, "workdir", logsafe.Dir(workdir), "error", err)
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
			slog.Warn("git session changes failed", "session_id", sessionID, "workdir", logsafe.Dir(workdir), "error", err)
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
