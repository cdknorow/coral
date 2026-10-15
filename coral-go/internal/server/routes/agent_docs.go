package routes

import (
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"
)

// AgentDocsHandler serves embedded API reference documentation.
type AgentDocsHandler struct {
	docsFS fs.FS
}

// NewAgentDocsHandler creates a handler that serves docs from the given filesystem.
func NewAgentDocsHandler(docsFS fs.FS) *AgentDocsHandler {
	return &AgentDocsHandler{docsFS: docsFS}
}

// List returns all available documentation files.
// GET /api/agent-docs
func (h *AgentDocsHandler) List(w http.ResponseWriter, r *http.Request) {
	entries, err := fs.ReadDir(h.docsFS, ".")
	if errors.Is(err, fs.ErrNotExist) {
		entries, err = nil, nil // a dev build without the docs folder lists none
	}
	if err != nil {
		errInternalServer(w, "failed to read docs directory")
		return
	}

	type docEntry struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	}

	docs := []docEntry{} // [] when there are none, never null: the Docs tab reads null as a failed load
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		title := strings.ReplaceAll(name, "-", " ")
		title = titleCase(title)
		docs = append(docs, docEntry{Name: name, Title: title})
	}
	sort.Slice(docs, func(i, j int) bool {
		// README first, then alphabetical
		if docs[i].Name == "README" {
			return true
		}
		if docs[j].Name == "README" {
			return false
		}
		return docs[i].Name < docs[j].Name
	})

	writeJSON(w, http.StatusOK, docs)
}

// Get returns the content of a single documentation file.
// GET /api/agent-docs/{name}
func (h *AgentDocsHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	// Sanitize: only allow alphanumeric, hyphens, underscores
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			errBadRequest(w, "invalid doc name")
			return
		}
	}

	filename := name + ".md"
	content, err := fs.ReadFile(h.docsFS, filename)
	if err != nil {
		errNotFound(w, "doc not found: "+name)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":    name,
		"content": string(content),
		"path":    filepath.Join("agent_docs", filename),
	})
}

// GetAll returns all documentation files concatenated.
// GET /api/agent-docs/all
func (h *AgentDocsHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	entries, err := fs.ReadDir(h.docsFS, ".")
	if err != nil {
		errInternalServer(w, "failed to read docs directory")
		return
	}

	var parts []string

	// README first
	if content, err := fs.ReadFile(h.docsFS, "README.md"); err == nil {
		parts = append(parts, string(content))
	}

	// Then all others alphabetically
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		content, err := fs.ReadFile(h.docsFS, name)
		if err != nil {
			continue
		}
		parts = append(parts, string(content))
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(strings.Join(parts, "\n\n---\n\n")))
}

// titleCase capitalizes the first letter of each word.
func titleCase(s string) string {
	prev := ' '
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(rune(prev)) || prev == ' ' {
			prev = r
			return unicode.ToTitle(r)
		}
		prev = r
		return r
	}, s)
}
