package routes

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/config"
)

// ThemesHandler handles theme CRUD endpoints.
type ThemesHandler struct {
	cfg       *config.Config
	themesDir string
}

func NewThemesHandler(cfg *config.Config) *ThemesHandler {
	return &ThemesHandler{
		cfg:       cfg,
		themesDir: filepath.Join(cfg.CoralDir(), "themes"),
	}
}

var safeNameRE = regexp.MustCompile(`[^a-zA-Z0-9\-_ ]`)

func (h *ThemesHandler) ensureDir() {
	os.MkdirAll(h.themesDir, 0755)
}

func (h *ThemesHandler) safePath(name string) string {
	safe := strings.TrimSpace(safeNameRE.ReplaceAllString(name, ""))
	if safe == "" {
		return ""
	}
	return filepath.Join(h.themesDir, safe+".json")
}

// ListThemes returns all saved themes.
// GET /api/themes
func (h *ThemesHandler) ListThemes(w http.ResponseWriter, r *http.Request) {
	h.ensureDir()
	entries, err := os.ReadDir(h.themesDir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"themes": []any{}})
		return
	}

	type themeInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Base        string `json:"base"`
	}

	var themes []themeInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(h.themesDir, e.Name()))
		if err != nil {
			continue
		}
		var t struct {
			Description string `json:"description"`
			Base        string `json:"base"`
		}
		if json.Unmarshal(data, &t) != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		themes = append(themes, themeInfo{Name: name, Description: t.Description, Base: t.Base})
	}
	sort.Slice(themes, func(i, j int) bool { return themes[i].Name < themes[j].Name })
	if themes == nil {
		themes = []themeInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"themes": themes})
}

// GetTheme returns a specific theme.
// GET /api/themes/{name}
func (h *ThemesHandler) GetTheme(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	path := h.safePath(name)
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid theme name"})
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Theme not found"})
		return
	}
	var theme map[string]any
	if json.Unmarshal(data, &theme) != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Invalid theme file"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "theme": theme})
}

// SaveTheme saves or updates a theme.
// PUT /api/themes/{name}
func (h *ThemesHandler) SaveTheme(w http.ResponseWriter, r *http.Request) {
	h.ensureDir()
	name := chi.URLParam(r, "name")
	path := h.safePath(name)
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid theme name"})
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	themeData := map[string]any{
		"description": body["description"],
		"base":        body["base"],
		"variables":   body["variables"],
	}
	data, _ := json.MarshalIndent(themeData, "", "  ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
}

// DeleteTheme deletes a theme.
// DELETE /api/themes/{name}
func (h *ThemesHandler) DeleteTheme(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	path := h.safePath(name)
	if path != "" {
		os.Remove(path)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ImportTheme imports a theme from an uploaded JSON file.
// POST /api/themes/import
func (h *ThemesHandler) ImportTheme(w http.ResponseWriter, r *http.Request) {
	h.ensureDir()
	r.ParseMultipartForm(10 << 20) // 10 MB
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file required"})
		return
	}
	defer file.Close()

	data, _ := io.ReadAll(file)
	var parsed map[string]any
	if json.Unmarshal(data, &parsed) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON file"})
		return
	}

	name, _ := parsed["name"].(string)
	if name == "" {
		name = strings.TrimSuffix(header.Filename, ".json")
	}
	safe := strings.TrimSpace(safeNameRE.ReplaceAllString(name, ""))
	if safe == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Could not determine theme name"})
		return
	}

	themeData := map[string]any{
		"description": parsed["description"],
		"base":        parsed["base"],
		"variables":   parsed["variables"],
	}
	out, _ := json.MarshalIndent(themeData, "", "  ")
	os.WriteFile(filepath.Join(h.themesDir, safe+".json"), out, 0644)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": safe})
}

// GetThemeVariables returns the theme variable definitions.
// GET /api/themes/variables
func (h *ThemesHandler) GetThemeVariables(w http.ResponseWriter, r *http.Request) {
	// Matches Python THEME_VARIABLES dict
	writeJSON(w, http.StatusOK, map[string]any{"groups": themeVariableGroups})
}

// GenerateTheme uses an LLM to generate theme colors.
// POST /api/themes/generate
func (h *ThemesHandler) GenerateTheme(w http.ResponseWriter, r *http.Request) {
	// TODO: call Claude CLI for AI theme generation
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "AI theme generation not yet ported"})
}

var themeVariableGroups = map[string]map[string]string{
	"Surface / Background": {
		"--bg-primary": "Primary background", "--bg-secondary": "Secondary background",
		"--bg-tertiary": "Tertiary background", "--bg-hover": "Hover background",
		"--bg-elevated": "Elevated surface", "--topbar-bg": "Top bar background",
		"--topbar-border": "Top bar border",
	},
	"Borders":          {"--border": "Border", "--border-light": "Light border"},
	"Text":             {"--text-primary": "Primary text", "--text-secondary": "Secondary text", "--text-muted": "Muted text"},
	"Accent / Brand":   {"--accent": "Accent", "--accent-dim": "Accent dim"},
	"Semantic Status":  {"--success": "Success", "--warning": "Warning", "--error": "Error"},
	"Agent Badges":     {"--badge-claude": "Claude badge", "--badge-gemini": "Gemini badge"},
}
