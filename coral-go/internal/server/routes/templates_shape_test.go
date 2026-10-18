package routes

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4627: the agent and command template routes share their handlers; each kind's replies keep their
// own keys: the list under the kind's name, a template with the kind's own frontmatter fields, the
// errors with the kind's empty list, and the usual 404 for a name the listing doesn't have.
func TestTemplateRepliesKeepEachKindsShape(t *testing.T) {
	md := base64.StdEncoding.EncodeToString([]byte("---\nname: Foo Bar\ndescription: does things\ntools: Read, Write\nmodel: opus\nallowed-tools: Bash(ls)\nargument-hint: [path]\n---\nTHE BODY\n"))
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/boom"):
			http.Error(w, "nope", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/foo.md"):
			_ = json.NewEncoder(w).Encode(map[string]any{"content": md})
		case strings.HasSuffix(r.URL.Path, "/components/agents"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "setup", "type": "dir"}, {"name": "boom", "type": "dir"}, {"name": "notes.txt", "type": "file"}})
		case strings.HasSuffix(r.URL.Path, "/components/commands"):
			// Commands have a category agents do not: a handler that asks the wrong kind refuses it.
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "setup", "type": "dir"}, {"name": "boom", "type": "dir"}, {"name": "cmdonly", "type": "dir"}, {"name": "notes.txt", "type": "file"}})
		default:
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "foo.md", "type": "file"}})
		}
	}))
	t.Cleanup(gh.Close)
	prev := githubAPIBase
	githubAPIBase = gh.URL + "/contents"
	t.Cleanup(func() { githubAPIBase = prev })
	h := NewTemplatesHandler()
	r := chi.NewRouter()
	r.Get("/api/templates/agents", h.ListAgentCategories)
	r.Get("/api/templates/agents/{category}", h.ListAgentsInCategory)
	r.Get("/api/templates/agents/{category}/{name}", h.GetAgentTemplate)
	r.Get("/api/templates/commands", h.ListCommandCategories)
	r.Get("/api/templates/commands/{category}", h.ListCommandsInCategory)
	r.Get("/api/templates/commands/{category}/{name}", h.GetCommandTemplate)
	get := func(path string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
		return rec.Code, out
	}

	for _, kind := range []string{"agents", "commands"} {
		code, cats := get("/api/templates/" + kind)
		assert.Equal(t, 200, code)
		wantCats := []any{map[string]any{"name": "setup", "type": "dir"}, map[string]any{"name": "boom", "type": "dir"}}
		if kind == "commands" {
			wantCats = append(wantCats, map[string]any{"name": "cmdonly", "type": "dir"})
		}
		assert.Equal(t, wantCats, cats["categories"], kind+" categories")

		// The category's own folder fails after the listing named it: 200, the error, the kind's empty list.
		code, broken := get("/api/templates/" + kind + "/boom")
		assert.Equal(t, 200, code)
		assert.NotEmpty(t, broken["error"], kind)
		assert.Equal(t, []any{}, broken[kind], kind+" broken category")

		code, list := get("/api/templates/" + kind + "/setup")
		assert.Equal(t, 200, code)
		assert.Equal(t, "setup", list["category"])
		items, _ := list[kind].([]any)
		require.Len(t, items, 1, kind+" list is under the kind's own name")
		assert.NotContains(t, list, map[string]string{"agents": "commands"}[kind], "and not under the other kind's")

		code, none := get("/api/templates/" + kind + "/nowhere")
		assert.Equal(t, 404, code)
		assert.Equal(t, "template_not_found", none["code"])
		code, noTemplate := get("/api/templates/" + kind + "/setup/other")
		assert.Equal(t, 404, code)
		assert.Equal(t, "template_not_found", noTemplate["code"])
	}

	code, onlyList := get("/api/templates/commands/cmdonly")
	assert.Equal(t, 200, code, "the list of a category only commands have")
	assert.Len(t, onlyList["commands"], 1)
	code, _ = get("/api/templates/agents/cmdonly")
	assert.Equal(t, 404, code)

	code, onlyCommands := get("/api/templates/commands/cmdonly/foo")
	assert.Equal(t, 200, code, "a template of a category only commands have")
	assert.Equal(t, "cmdonly", onlyCommands["category"])
	code, _ = get("/api/templates/agents/cmdonly/foo")
	assert.Equal(t, 404, code, "and agents do not")

	code, agent := get("/api/templates/agents/setup/foo")
	assert.Equal(t, 200, code)
	assert.Equal(t, map[string]any{"name": "Foo Bar", "description": "does things", "tools": "Read, Write", "model": "opus", "body": "THE BODY", "category": "setup"}, agent)
	code, command := get("/api/templates/commands/setup/foo.md")
	assert.Equal(t, 200, code)
	assert.Equal(t, map[string]any{"name": "Foo Bar", "description": "does things", "allowed_tools": "Bash(ls)", "argument_hint": "[path]", "body": "THE BODY", "category": "setup"}, command)

	// A listing that cannot be read: 200 with the error and the kind's empty list.
	gh.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusInternalServerError) })
	h2 := NewTemplatesHandler()
	r2 := chi.NewRouter()
	r2.Get("/api/templates/agents", h2.ListAgentCategories)
	r2.Get("/api/templates/commands/{category}", h2.ListCommandsInCategory)
	for path, key := range map[string]string{"/api/templates/agents": "categories", "/api/templates/commands/setup": "commands"} {
		rec := httptest.NewRecorder()
		r2.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		assert.Equal(t, 200, rec.Code, path)
		assert.NotEmpty(t, out["error"], path)
		assert.Equal(t, []any{}, out[key], path)
	}
}
