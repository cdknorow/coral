// Searching a session's files (moved out of sessions_files.go, #5003; no behaviour change).

package routes

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

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
