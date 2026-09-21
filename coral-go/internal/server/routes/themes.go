package routes

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/executil"
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

// bundledThemes maps theme name → JSON content for themes shipped with Coral.
var bundledThemes = map[string]string{
	"GhostV3": `{
  "description": "Dark indigo theme with soft pastels and cool blue accents",
  "base": "dark",
  "variables": {
    "--bg-primary": "#0a0e27",
    "--bg-secondary": "#1a1f3a",
    "--bg-tertiary": "#2a2f4a",
    "--bg-hover": "#35394f",
    "--bg-elevated": "#3a3f5a",
    "--topbar-bg": "#1a1f3a",
    "--topbar-border": "#4a4f6a",
    "--border": "#4a4f6a",
    "--border-light": "#5a5f7a",
    "--text-primary": "#d0d4e8",
    "--text-secondary": "#8a8fa0",
    "--text-muted": "#5a5f75",
    "--accent": "#7dd3fc",
    "--accent-dim": "#38bdf8",
    "--success": "#86efac",
    "--warning": "#fbbf24",
    "--error": "#f87171",
    "--badge-claude": "#a78bfa",
    "--badge-gemini": "#f472b6",
    "--sh-keyword": "#a78bfa",
    "--sh-string": "#86efac",
    "--sh-comment": "#6b7280",
    "--sh-number": "#fbbf24",
    "--sh-builtin": "#7dd3fc",
    "--sh-decorator": "#f472b6",
    "--diff-add-bg": "#1d3a1a",
    "--diff-add-color": "#86efac",
    "--diff-del-bg": "#3a1d1d",
    "--diff-del-color": "#f87171",
    "--color-tool-read": "#60a5fa",
    "--color-tool-write": "#34d399",
    "--color-tool-edit": "#fbbf24",
    "--color-tool-bash": "#f87171",
    "--color-tool-grep": "#a78bfa",
    "--color-tool-web": "#7dd3fc",
    "--color-tool-status": "#60a5fa",
    "--color-tool-goal": "#a78bfa",
    "--color-tool-stop": "#f87171",
    "--chat-human-bg": "#1a1f3a",
    "--chat-human-color": "#d0d4e8",
    "--chat-assistant-bg": "#161b22",
    "--xterm-background": "#0a0e27",
    "--xterm-foreground": "#d0d4e8",
    "--xterm-cursor": "#7dd3fc",
    "--xterm-selection-background": "#35394f",
    "--xterm-black": "#1a1f3a",
    "--xterm-red": "#f87171",
    "--xterm-green": "#86efac",
    "--xterm-yellow": "#fbbf24",
    "--xterm-blue": "#60a5fa",
    "--xterm-magenta": "#a78bfa",
    "--xterm-cyan": "#7dd3fc",
    "--xterm-white": "#d0d4e8",
    "--xterm-bright-black": "#4a4f6a",
    "--xterm-bright-red": "#fb7185",
    "--xterm-bright-green": "#6ee7b7",
    "--xterm-bright-yellow": "#fcd34d",
    "--xterm-bright-blue": "#93c5fd",
    "--xterm-bright-magenta": "#d8b4fe",
    "--xterm-bright-cyan": "#a5f3fc",
    "--xterm-bright-white": "#f1f5f9",
    "--mb-bg": "#1a1f3a",
    "--mb-text": "#e0e0e0",
    "--mb-text-bright": "#f0f0f0",
    "--mb-heading": "#88c0d0",
    "--mb-code-bg": "#0a0e27",
    "--board-msg-bg": "#1e2233"
  }
}`,
	"Obsidian": `{
  "description": "Deep black theme with purple accents and emerald highlights",
  "base": "dark",
  "variables": {
    "--bg-primary": "#09090b",
    "--bg-secondary": "#0c0c0f",
    "--bg-tertiary": "#18181b",
    "--bg-hover": "#27272a",
    "--bg-elevated": "#1e1e22",
    "--topbar-bg": "rgba(9, 9, 11, 0.95)",
    "--topbar-border": "rgba(39, 39, 42, 0.6)",
    "--border": "#27272a",
    "--border-light": "#3f3f46",
    "--text-primary": "#fafafa",
    "--text-secondary": "#a1a1aa",
    "--text-muted": "#52525b",
    "--accent": "#a78bfa",
    "--accent-dim": "#7c3aed",
    "--success": "#34d399",
    "--warning": "#ffb781",
    "--error": "#ffb4ab",
    "--badge-claude": "#7c3aed",
    "--badge-gemini": "#2563eb",
    "--sh-keyword": "#f87171",
    "--sh-string": "#a6c8ff",
    "--sh-comment": "#71717a",
    "--sh-number": "#93c5fd",
    "--sh-builtin": "#ffb781",
    "--sh-decorator": "#c4b5fd",
    "--diff-add-bg": "rgba(52, 211, 153, 0.15)",
    "--diff-add-color": "#34d399",
    "--diff-del-bg": "rgba(255, 180, 171, 0.15)",
    "--diff-del-color": "#ffb4ab",
    "--color-tool-read": "#a78bfa",
    "--color-tool-write": "#34d399",
    "--color-tool-edit": "#ffb781",
    "--color-tool-bash": "#c4b5fd",
    "--color-tool-grep": "#93c5fd",
    "--color-tool-web": "#ffb781",
    "--color-tool-status": "#a78bfa",
    "--color-tool-goal": "#34d399",
    "--color-tool-stop": "#ffb4ab",
    "--chat-human-bg": "#1e1b2e",
    "--chat-human-color": "#fafafa",
    "--chat-assistant-bg": "#18181b",
    "--xterm-background": "#09090b",
    "--xterm-foreground": "#fafafa",
    "--xterm-cursor": "#a78bfa",
    "--xterm-selection-background": "#3f3f46",
    "--xterm-black": "#52525b",
    "--xterm-red": "#ffb4ab",
    "--xterm-green": "#34d399",
    "--xterm-yellow": "#ffb781",
    "--xterm-blue": "#a6c8ff",
    "--xterm-magenta": "#c4b5fd",
    "--xterm-cyan": "#5eead4",
    "--xterm-white": "#fafafa",
    "--xterm-bright-black": "#71717a",
    "--xterm-bright-red": "#fecaca",
    "--xterm-bright-green": "#6ee7b7",
    "--xterm-bright-yellow": "#fbbf24",
    "--xterm-bright-blue": "#93c5fd",
    "--xterm-bright-magenta": "#ddd6fe",
    "--xterm-bright-cyan": "#99f6e4",
    "--xterm-bright-white": "#fafafa",
    "--mb-bg": "#121215",
    "--mb-text": "#e4e4e7",
    "--mb-text-bright": "#fafafa",
    "--mb-heading": "#a6c8ff",
    "--mb-code-bg": "#09090b",
    "--board-msg-bg": "transparent"
  }
}`,
	"Dark": `{
  "description": "Dark theme with cool blue accents",
  "base": "dark",
  "variables": {
    "--accent": "#5173a9",
    "--accent-dim": "#5173a9",
    "--badge-claude": "#0061ff",
    "--badge-gemini": "#ea8226",
    "--border": "#404040",
    "--border-light": "#323232",
    "--chat-assistant-bg": "#323232",
    "--chat-human-bg": "#2a3f5f",
    "--chat-human-color": "#ffffff",
    "--diff-add-bg": "#1d3a1d",
    "--diff-add-color": "#7ef07e",
    "--diff-del-bg": "#3a1d1d",
    "--diff-del-color": "#f07e7e",
    "--board-msg-bg": "transparent",
    "--mb-bg": "#303030",
    "--mb-code-bg": "#292929",
    "--mb-heading": "#88c0d0",
    "--mb-text": "#e0e0e0",
    "--mb-text-bright": "#f0f0f0",
    "--error": "#e74c3c",
    "--success": "#31a24c",
    "--warning": "#f5a623",
    "--bg-elevated": "#404040",
    "--bg-hover": "#3a3a3a",
    "--bg-primary": "#1a1a1a",
    "--bg-secondary": "#262626",
    "--bg-tertiary": "#323232",
    "--topbar-bg": "#0f0f0f",
    "--topbar-border": "#3a3a3a",
    "--sh-builtin": "#8be9fd",
    "--sh-comment": "#6272a4",
    "--sh-decorator": "#50fa7b",
    "--sh-keyword": "#ff79c6",
    "--sh-number": "#bd93f9",
    "--sh-string": "#f1fa8c",
    "--xterm-background": "#1a1a1a",
    "--xterm-black": "#262626",
    "--xterm-blue": "#8be9fd",
    "--xterm-bright-black": "#6272a4",
    "--xterm-bright-blue": "#a1e7fd",
    "--xterm-bright-cyan": "#a1e7fd",
    "--xterm-bright-green": "#69fb9e",
    "--xterm-bright-magenta": "#ff92d0",
    "--xterm-bright-red": "#ff6e6e",
    "--xterm-bright-white": "#ffffff",
    "--xterm-bright-yellow": "#f4fb8c",
    "--xterm-cursor": "#0061ff",
    "--xterm-cyan": "#8be9fd",
    "--xterm-foreground": "#ffffff",
    "--xterm-green": "#50fa7b",
    "--xterm-magenta": "#ff79c6",
    "--xterm-red": "#ff5555",
    "--xterm-selection-background": "#0061ff",
    "--xterm-white": "#f8f8f2",
    "--xterm-yellow": "#f1fa8c",
    "--text-muted": "#808080",
    "--text-primary": "#ffffff",
    "--text-secondary": "#b3b3b3",
    "--color-tool-bash": "#50fa7b",
    "--color-tool-edit": "#ff79c6",
    "--color-tool-goal": "#31a24c",
    "--color-tool-grep": "#bd93f9",
    "--color-tool-read": "#8be9fd",
    "--color-tool-status": "#f5a623",
    "--color-tool-stop": "#e74c3c",
    "--color-tool-web": "#0061ff",
    "--color-tool-write": "#f1fa8c",
    "--d2h-code-bg": "#262626",
    "--d2h-del-bg": "#3a1d1d",
    "--d2h-del-gutter-bg": "#2a0d0d",
    "--d2h-del-highlight": "#e74c3c",
    "--d2h-empty-bg": "#323232",
    "--d2h-gutter-bg": "#1a1a1a",
    "--d2h-hunk-bg": "#0061ff",
    "--d2h-ins-bg": "#1d3a1d",
    "--d2h-ins-gutter-bg": "#0d2a0d",
    "--d2h-ins-highlight": "#31a24c"
  }
}`,
}

// SeedBundledThemes copies bundled themes to the user themes directory if they
// don't already exist. Matches Python's seed_bundled_themes().
func (h *ThemesHandler) SeedBundledThemes() {
	h.ensureDir()
	for name, content := range bundledThemes {
		dest := filepath.Join(h.themesDir, name+".json")
		if _, err := os.Stat(dest); err == nil {
			continue // already exists
		}
		if err := os.WriteFile(dest, []byte(content), 0644); err != nil {
			log.Printf("Warning: failed to seed bundled theme %s: %v", name, err)
			continue
		}
		log.Printf("Seeded bundled theme: %s", name)
	}
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
	writeJSON(w, http.StatusOK, map[string]any{"themes": emptyIfNil(themes)})
}

// GetTheme returns a specific theme.
// GET /api/themes/{name}
func (h *ThemesHandler) GetTheme(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	path := h.safePath(name)
	if path == "" {
		errBadRequest(w, "Invalid theme name")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		errNotFound(w, "Theme not found")
		return
	}
	var theme map[string]any
	if json.Unmarshal(data, &theme) != nil {
		errInternalServer(w, "Invalid theme file")
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
		errBadRequest(w, "Invalid theme name")
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	themeData := map[string]any{
		"description": body["description"],
		"base":        body["base"],
		"variables":   body["variables"],
	}
	data, _ := json.MarshalIndent(themeData, "", "  ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		errInternalServer(w, err.Error())
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
		errBadRequest(w, "file required")
		return
	}
	defer file.Close()

	data, _ := io.ReadAll(file)
	var parsed map[string]any
	if json.Unmarshal(data, &parsed) != nil {
		errBadRequest(w, "Invalid JSON file")
		return
	}

	name, _ := parsed["name"].(string)
	if name == "" {
		name = strings.TrimSuffix(header.Filename, ".json")
	}
	safe := strings.TrimSpace(safeNameRE.ReplaceAllString(name, ""))
	if safe == "" {
		errBadRequest(w, "Could not determine theme name")
		return
	}

	themeData := map[string]any{
		"description": parsed["description"],
		"base":        parsed["base"],
		"variables":   parsed["variables"],
	}
	out, err := json.MarshalIndent(themeData, "", "  ")
	if err != nil {
		errBadRequest(w, "Failed to encode theme")
		return
	}
	if err := os.WriteFile(filepath.Join(h.themesDir, safe+".json"), out, 0644); err != nil {
		http.Error(w, "Failed to save theme", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": safe})
}

// GetThemeVariables returns the theme variable definitions.
// GET /api/themes/variables
func (h *ThemesHandler) GetThemeVariables(w http.ResponseWriter, r *http.Request) {
	// Matches Python THEME_VARIABLES dict
	writeJSON(w, http.StatusOK, map[string]any{"groups": themeVariableGroups})
}

// GenerateTheme uses an LLM to generate theme colors from a text description.
// POST /api/themes/generate
func (h *ThemesHandler) GenerateTheme(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Description string `json:"description"`
		Base        string `json:"base"`
		AgentType   string `json:"agent_type"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Description) == "" {
		errBadRequest(w, "Description is required")
		return
	}
	if body.Base == "" {
		body.Base = "dark"
	}

	// Find an available LLM CLI — try requested type first, then fall back
	cliPath, cliArgs := resolveThemeCLI(body.AgentType)
	if cliPath == "" {
		errInternalServer(w, "No LLM CLI found — install Claude Code (npm install -g @anthropic-ai/claude-code) or Gemini CLI")
		return
	}

	// Build the variable list for the prompt
	var varList strings.Builder
	for groupName, vars := range themeVariableGroups {
		varList.WriteString("\n" + groupName + ":\n")
		for cssVar, label := range vars {
			varList.WriteString("  " + cssVar + " — " + label + "\n")
		}
	}

	prompt := generatePrompt + varList.String() + "\n" +
		"The theme should be based on a " + body.Base + " color scheme.\n" +
		"User's description: " + body.Description + "\n\n" +
		"Respond with ONLY the JSON object."

	args := append(cliArgs, prompt)
	cmd := executil.Command(r.Context(), cliPath, args...)
	output, err := cmd.Output()
	if err != nil {
		errInternalServer(w, "Claude CLI failed: "+err.Error())
		return
	}

	// Strip markdown fences if present
	raw := strings.TrimSpace(string(output))
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		var cleaned []string
		for _, line := range lines {
			if !strings.HasPrefix(strings.TrimSpace(line), "```") {
				cleaned = append(cleaned, line)
			}
		}
		raw = strings.TrimSpace(strings.Join(cleaned, "\n"))
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to parse LLM response as JSON", "raw": raw})
		return
	}

	variables, _ := result["variables"].(map[string]any)
	if variables == nil {
		variables, _ = result["variables"].(map[string]any)
		if variables == nil {
			// Response might be flat (variables at root level)
			variables = result
		}
	}

	name, _ := result["name"].(string)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "variables": variables, "name": name})
}

// resolveThemeCLI finds an available LLM CLI for theme generation.
// Tries the requested agent type first, then falls back to others.
func resolveThemeCLI(agentType string) (path string, args []string) {
	type cliOption struct {
		binary string
		args   []string
	}
	options := []cliOption{
		{"claude", []string{"--print", "--model", "haiku", "--no-session-persistence"}},
		{"gemini", []string{"--print"}},
		{"codex", []string{"--print"}},
	}

	// Try requested type first
	if agentType != "" {
		for _, opt := range options {
			if opt.binary == agentType {
				if p, err := exec.LookPath(opt.binary); err == nil {
					return p, opt.args
				}
			}
		}
	}

	// Fall back to any available CLI
	for _, opt := range options {
		if p, err := exec.LookPath(opt.binary); err == nil {
			return p, opt.args
		}
	}
	return "", nil
}

const generatePrompt = `You are a UI theme designer. Given a description of a color theme, generate a complete set of CSS color values for a web dashboard.

You MUST respond with ONLY a valid JSON object — no markdown, no explanation, no code fences. The JSON must have this exact structure:

{
  "name": "A short creative name for this theme (2-4 words)",
  "variables": {
    "--css-variable-name": "#hexcolor",
    ...
  }
}

Here are the CSS variables you must provide values for, grouped by category:
`

var themeVariableGroups = map[string]map[string]string{
	"Surface / Background": {
		"--bg-primary": "Primary background", "--bg-secondary": "Secondary background",
		"--bg-tertiary": "Tertiary background", "--bg-hover": "Hover background",
		"--bg-elevated": "Elevated surface", "--topbar-bg": "Top bar background",
		"--topbar-border": "Top bar border",
	},
	"Borders":         {"--border": "Border", "--border-light": "Light border"},
	"Text":            {"--text-primary": "Primary text", "--text-secondary": "Secondary text", "--text-muted": "Muted text"},
	"Accent / Brand":  {"--accent": "Accent", "--accent-dim": "Accent dim"},
	"Semantic Status": {"--success": "Success", "--warning": "Warning", "--error": "Error"},
	"Agent Badges":    {"--badge-claude": "Claude badge", "--badge-gemini": "Gemini badge"},
	"Syntax Highlighting": {
		"--sh-keyword": "Keyword", "--sh-string": "String", "--sh-comment": "Comment",
		"--sh-number": "Number", "--sh-builtin": "Builtin", "--sh-decorator": "Decorator",
	},
	"Diff": {
		"--diff-add-bg": "Addition background", "--diff-add-color": "Addition text",
		"--diff-del-bg": "Deletion background", "--diff-del-color": "Deletion text",
	},
	"Tool / Event Colors": {
		"--color-tool-read": "Read tool", "--color-tool-write": "Write tool",
		"--color-tool-edit": "Edit tool", "--color-tool-bash": "Bash tool",
		"--color-tool-grep": "Grep tool", "--color-tool-web": "Web tool",
		"--color-tool-status": "Status event", "--color-tool-goal": "Goal event",
		"--color-tool-stop": "Stop event",
	},
	"Chat": {
		"--chat-prose-human-bg":    "Your message background (chat view)",
		"--chat-prose-inline-code": "Inline code text (chat view)",
		"--chat-human-bg":          "Your message background (history panel)", "--chat-human-color": "Your message text",
		"--chat-assistant-bg": "Assistant message background (history panel)",
	},
	"Terminal (xterm)": {
		"--xterm-background": "Background", "--xterm-foreground": "Foreground",
		"--xterm-cursor": "Cursor", "--xterm-selection-background": "Selection background",
		"--xterm-black": "Black", "--xterm-red": "Red", "--xterm-green": "Green",
		"--xterm-yellow": "Yellow", "--xterm-blue": "Blue", "--xterm-magenta": "Magenta",
		"--xterm-cyan": "Cyan", "--xterm-white": "White",
		"--xterm-bright-black": "Bright black", "--xterm-bright-red": "Bright red",
		"--xterm-bright-green": "Bright green", "--xterm-bright-yellow": "Bright yellow",
		"--xterm-bright-blue": "Bright blue", "--xterm-bright-magenta": "Bright magenta",
		"--xterm-bright-cyan": "Bright cyan", "--xterm-bright-white": "Bright white",
	},
	"Message Board": {
		"--mb-bg": "Message background", "--mb-text": "Body text",
		"--mb-text-bright": "Bold/emphasis text", "--mb-heading": "Heading color",
		"--mb-code-bg":   "Code block background",
		"--board-msg-bg": "Board panel message background",
	},
}
