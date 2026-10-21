// Resolving a file reference to a path inside the session's folder (moved out of sessions_files.go, #5003; no behaviour change).

package routes

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/subgentic/coral/internal/safefs"
)

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
	fp = fileURLPath(fp, runtime.GOOS == "windows")

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

// fileURLPath is the path a file: URL names (fp otherwise), already percent-decoded:
// file://localhost/p and file:///p are /p. On Windows a drive path loses the slash before its
// letter (file:///C:/x/y.go is C:/x/y.go, not a /C:/... joined under the repository and refused,
// #59, QA #28 C3) and file://server/share/x is the UNC path \\server\share\x, which the
// containment checks then allow or refuse like any other path. Elsewhere file://host/p keeps
// what it always gave, host/p.
func fileURLPath(fp string, windows bool) string {
	lower := strings.ToLower(fp)
	switch {
	case strings.HasPrefix(lower, "file://localhost/"):
		fp = fp[len("file://localhost"):]
	case strings.HasPrefix(lower, "file:///"):
		fp = fp[len("file://"):]
	case strings.HasPrefix(lower, "file://"):
		rest := fp[len("file://"):]
		// file://server/share/x: a host name (no colon or backslash: file://C:/x is the drive
		// path, as before) and then a path. Anything else stays as it always was.
		host, _, hasPath := strings.Cut(rest, "/")
		if windows && hasPath && host != "" && !strings.ContainsAny(host, `:\`) {
			return `\\` + strings.ReplaceAll(rest, "/", `\`)
		}
		return rest
	default:
		return fp
	}
	if windows && len(fp) > 1 && fp[0] == '/' && driveLetterPath(fp[1:]) {
		fp = fp[1:]
	}
	return fp
}

// driveLetterPath reports whether p starts with a drive letter, a colon and a slash (C:/ or C:\).
func driveLetterPath(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\') &&
		(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z')
}
