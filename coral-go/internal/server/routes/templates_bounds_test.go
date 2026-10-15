package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// templatesRig: the templates routes in front of a fake GitHub that counts its requests.
func templatesRig(t *testing.T) (*TemplatesHandler, http.Handler, *atomic.Int64, *[]string) {
	t.Helper()
	var calls atomic.Int64
	var paths []string
	var mu sync.Mutex
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".md") {
			_ = json.NewEncoder(w).Encode(map[string]any{"content": "LS0tCm5hbWU6IHgKLS0tCmJvZHk="})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "a.md", "type": "file"}})
	}))
	t.Cleanup(gh.Close)
	prev := githubAPIBase
	githubAPIBase = gh.URL + "/contents"
	t.Cleanup(func() { githubAPIBase = prev })
	h := NewTemplatesHandler()
	r := chi.NewRouter()
	r.Get("/api/templates/agents/{category}", h.ListAgentsInCategory)
	r.Get("/api/templates/agents/{category}/{name}", h.GetAgentTemplate)
	r.Get("/api/templates/commands/{category}", h.ListCommandsInCategory)
	r.Get("/api/templates/commands/{category}/{name}", h.GetCommandTemplate)
	return h, r, &calls, &paths
}

// #3619 (hub audit H2): a category or name becomes a segment of the outbound GitHub URL, so
// anything but letters, digits, '.', '_' and '-' (at most 100), or "." and "..", is refused
// with 400 and never fetched.
func TestTemplateSegmentsAreCheckedBeforeTheOutboundCall(t *testing.T) {
	_, r, calls, paths := templatesRig(t)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	bad := []string{"%3Fref%3Dx", "%23frag", "%2e%2e", "..", ".", "a%20b", "a%2Fb", "%25", strings.Repeat("a", 5000), "a%00"}
	for _, kind := range []string{"agents", "commands"} {
		for _, seg := range bad {
			rec := get("/api/templates/" + kind + "/" + seg)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "%s category %q", kind, seg)
			assert.Contains(t, rec.Body.String(), "bad_template_name")
			rec = get("/api/templates/" + kind + "/ok/" + seg)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "%s name %q", kind, seg)
			rec = get("/api/templates/" + kind + "/" + seg + "/ok")
			assert.Equal(t, http.StatusBadRequest, rec.Code, "%s category %q with a name", kind, seg)
		}
	}
	assert.Zero(t, calls.Load(), "nothing refused reached GitHub: %v", *paths)

	for _, path := range []string{"/api/templates/agents/data-ai", "/api/templates/agents/data-ai/ml.engineer_2", "/api/templates/commands/setup/x.md"} {
		rec := get(path)
		require.Equal(t, http.StatusOK, rec.Code, path)
		assert.NotContains(t, rec.Body.String(), `"error"`, path)
	}
	assert.Equal(t, []string{
		"/contents/cli-tool/components/agents/data-ai",
		"/contents/cli-tool/components/agents/data-ai/ml.engineer_2.md",
		"/contents/cli-tool/components/commands/setup/x.md",
	}, *paths)
}

// The cache stays within its entry and byte caps, and a failed fetch is remembered briefly,
// so repeating a miss doesn't call GitHub again.
func TestTemplateCacheIsBounded(t *testing.T) {
	h, r, calls, _ := templatesRig(t)
	for i := 0; i < templateCacheMaxEntries+50; i++ {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/api/templates/agents/c%d", i), nil))
		require.Equal(t, http.StatusOK, rec.Code)
	}
	h.mu.RLock()
	assert.LessOrEqual(t, len(h.cache), templateCacheMaxEntries)
	h.mu.RUnlock()

	big := templateCacheMaxBytes / 4
	for i := 0; i < 10; i++ {
		h.cacheSet(fmt.Sprintf("big%d", i), cacheEntry{fetchedAt: time.Now(), data: "x", size: big})
	}
	h.cacheSet("huge", cacheEntry{fetchedAt: time.Now(), data: "x", size: templateCacheMaxBytes + 1})
	h.mu.RLock()
	total := 0
	for _, e := range h.cache {
		total += e.size
	}
	_, huge := h.cache["huge"]
	assert.LessOrEqual(t, total, templateCacheMaxBytes)
	assert.Equal(t, total, h.bytes, "the running byte count matches")
	assert.False(t, huge, "an entry bigger than the cap isn't stored")
	h.mu.RUnlock()

	before := calls.Load()
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/templates/agents/missing", nil))
		assert.Contains(t, rec.Body.String(), "GitHub API returned 404")
	}
	assert.Equal(t, before+1, calls.Load(), "a miss is fetched once and then remembered")
}
