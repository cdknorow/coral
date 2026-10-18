package routes

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	templateRepo     = "davila7/claude-code-templates"
	templateCacheTTL = time.Hour
	// A failed fetch is remembered briefly, so repeating it doesn't spend GitHub's
	// unauthenticated quota (60 an hour per address) on the same miss (#3619, hub audit H2).
	templateFailTTL = time.Minute
	// The cache is bounded in entries and bytes: every distinct name a viewer sends would
	// otherwise add one (#3619, hub audit H2).
	templateCacheMaxEntries = 256
	templateCacheMaxBytes   = 16 << 20
	templateMaxResponse     = 2 << 20
)

// githubAPIBase is the GitHub Contents API folder the templates come from (a variable so
// tests can point it at a local server).
var githubAPIBase = "https://api.github.com/repos/" + templateRepo + "/contents"

// templateSegmentRe is what a category or template name may be: it becomes one segment of
// the outbound GitHub URL, so '/', '?', '#', '%', spaces and anything else that would change
// the request are refused (#3619, hub audit H2). "." and ".." are refused separately.
var templateSegmentRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

func validTemplateSegment(s string) bool {
	return templateSegmentRe.MatchString(s) && s != "." && s != ".."
}

// templateSegments reads the route's category (and name, when the route has one) and answers
// 400 when one isn't a valid segment.
func templateSegments(w http.ResponseWriter, r *http.Request, withName bool) (category, name string, ok bool) {
	category = chi.URLParam(r, "category")
	if withName {
		name = chi.URLParam(r, "name")
	}
	if !validTemplateSegment(category) || (withName && !validTemplateSegment(name)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a template category or name has only letters, digits, '.', '_' and '-' (at most 100)", "code": "bad_template_name"})
		return "", "", false
	}
	return category, name, true
}

// cacheEntry stores a cached GitHub API response, or a failure.
type cacheEntry struct {
	fetchedAt time.Time
	data      interface{}
	err       error
	size      int
}

func (e cacheEntry) fresh(now time.Time) bool {
	ttl := templateCacheTTL
	if e.err != nil {
		ttl = templateFailTTL
	}
	return now.Sub(e.fetchedAt) <= ttl
}

// TemplatesHandler proxies the davila7/claude-code-templates GitHub repo with in-memory caching.
type TemplatesHandler struct {
	mu       sync.RWMutex
	cache    map[string]cacheEntry
	bytes    int
	client   *http.Client
	inflight map[string]*templateFetch // one outbound call per URL at a time
}

// templateFetch is an outbound call others wait on instead of making their own.
type templateFetch struct {
	done chan struct{}
	data interface{}
	err  error
}

// NewTemplatesHandler creates a new TemplatesHandler.
func NewTemplatesHandler() *TemplatesHandler {
	return &TemplatesHandler{
		cache:    make(map[string]cacheEntry),
		client:   &http.Client{Timeout: 15 * time.Second},
		inflight: make(map[string]*templateFetch),
	}
}

func (h *TemplatesHandler) cacheGet(key string) (cacheEntry, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	entry, ok := h.cache[key]
	if !ok || !entry.fresh(time.Now()) {
		return cacheEntry{}, false
	}
	return entry, true
}

// cacheSet stores an entry, first dropping stale entries and then the oldest until it fits.
// An entry larger than the whole byte cap isn't stored.
func (h *TemplatesHandler) cacheSet(key string, e cacheEntry) {
	if e.size > templateCacheMaxBytes {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.cache[key]; ok {
		h.bytes -= old.size
		delete(h.cache, key)
	}
	now := time.Now()
	for k, v := range h.cache {
		if !v.fresh(now) {
			h.bytes -= v.size
			delete(h.cache, k)
		}
	}
	for len(h.cache) >= templateCacheMaxEntries || h.bytes+e.size > templateCacheMaxBytes {
		oldest := ""
		for k, v := range h.cache {
			if oldest == "" || v.fetchedAt.Before(h.cache[oldest].fetchedAt) {
				oldest = k
			}
		}
		if oldest == "" {
			break // nothing left to drop: never spin under the lock
		}
		h.bytes -= h.cache[oldest].size
		delete(h.cache, oldest)
	}
	h.cache[key] = e
	h.bytes += e.size
}

// githubFetch fetches a path from the GitHub Contents API with caching. Callers pass only
// fixed folders and segments that passed validTemplateSegment.
func (h *TemplatesHandler) githubFetch(path string) (interface{}, error) {
	url := fmt.Sprintf("%s/%s", githubAPIBase, path)
	if cached, ok := h.cacheGet(url); ok {
		return cached.data, cached.err
	}
	// Concurrent misses on one URL share a single outbound call.
	h.mu.Lock()
	if f, ok := h.inflight[url]; ok {
		h.mu.Unlock()
		<-f.done
		return f.data, f.err
	}
	f := &templateFetch{done: make(chan struct{})}
	h.inflight[url] = f
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.inflight, url)
		h.mu.Unlock()
		close(f.done)
	}()

	data, size, err := h.fetch(url)
	f.data, f.err = data, err
	if err != nil {
		h.cacheSet(url, cacheEntry{fetchedAt: time.Now(), err: err, size: len(err.Error())})
		return nil, err
	}
	h.cacheSet(url, cacheEntry{fetchedAt: time.Now(), data: data, size: size})
	return data, nil
}

// errTemplateUnknown: the category or template isn't in the repository's listing.
var errTemplateUnknown = errors.New("no such template")

// known reports whether category (and filename, when given) appears in the repository's
// listings, read through the same cache. Only names GitHub listed are ever fetched, so a
// viewer sending many made-up names costs no outbound call for any of them, and the
// unauthenticated quota (60 an hour) stays the dashboard's (#3619, Security's SHOULD on H2).
func (h *TemplatesHandler) known(kind, category, filename string) error {
	data, err := h.githubFetch("cli-tool/components/" + kind)
	if err != nil {
		return err
	}
	found := false
	for _, it := range extractDirItems(data, "dir") {
		if it["name"] == category {
			found = true
			break
		}
	}
	if !found {
		return errTemplateUnknown
	}
	if filename == "" {
		return nil
	}
	data, err = h.githubFetch("cli-tool/components/" + kind + "/" + category)
	if err != nil {
		return err
	}
	for _, it := range extractFileItems(data) {
		if it["filename"] == filename {
			return nil
		}
	}
	return errTemplateUnknown
}

// refuseUnknown answers for a name known refused: 404 for one not in the listing, or the
// usual error shape when the listing itself couldn't be read.
func refuseUnknown(w http.ResponseWriter, err error, empty map[string]interface{}) {
	if errors.Is(err, errTemplateUnknown) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error(), "code": "template_not_found"})
		return
	}
	out := map[string]interface{}{"error": err.Error()}
	for k, v := range empty {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *TemplatesHandler) fetch(url string) (interface{}, int, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, templateMaxResponse+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > templateMaxResponse {
		return nil, 0, fmt.Errorf("GitHub API response is larger than %d bytes", templateMaxResponse)
	}

	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, 0, err
	}
	return data, len(body), nil
}

// parseFrontmatter parses YAML-like frontmatter from markdown content without a YAML library.
func parseFrontmatter(content string) (map[string]string, string) {
	parts := strings.SplitN(content, "---", 3)
	if len(parts) >= 3 {
		meta := make(map[string]string)
		for _, line := range strings.Split(strings.TrimSpace(parts[1]), "\n") {
			idx := strings.Index(line, ":")
			if idx < 0 {
				continue
			}
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			val = strings.Trim(strings.Trim(val, "\""), "'")
			if val != "" {
				meta[key] = val
			}
		}
		return meta, strings.TrimSpace(parts[2])
	}
	return map[string]string{}, content
}

// decodeGitHubFileContent decodes base64 file content from a GitHub API response.
func decodeGitHubFileContent(data map[string]interface{}) string {
	contentB64, _ := data["content"].(string)
	if contentB64 == "" {
		return ""
	}
	// GitHub returns base64 with embedded newlines; strip them first (matches Python b64decode behavior)
	cleaned := strings.ReplaceAll(contentB64, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return ""
	}
	return string(decoded)
}

// extractDirItems extracts directory entries of a given type from a GitHub Contents API response.
func extractDirItems(data interface{}, filterType string) []map[string]string {
	items, ok := data.([]interface{})
	if !ok {
		return nil
	}
	var result []map[string]string
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		name, _ := m["name"].(string)
		if itemType == filterType {
			result = append(result, map[string]string{"name": name, "type": itemType})
		}
	}
	return result
}

// extractFileItems extracts .md file entries from a GitHub Contents API response.
func extractFileItems(data interface{}) []map[string]string {
	items, ok := data.([]interface{})
	if !ok {
		return nil
	}
	var result []map[string]string
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		itemType, _ := m["type"].(string)
		name, _ := m["name"].(string)
		if itemType == "file" && strings.HasSuffix(name, ".md") {
			result = append(result, map[string]string{
				"name":     strings.TrimSuffix(name, ".md"),
				"filename": name,
			})
		}
	}
	return result
}

// ── Agent and command templates ─────────────────────────────────────
//
// The two kinds are the same three requests over different folders of the components repository
// ("agents" and "commands"): the categories, the templates in one, and one template with its
// frontmatter. The list's key in the reply is the kind's name; a template's own fields differ.

// ListAgentCategories lists agent template categories (top-level directories).
// GET /api/templates/agents
func (h *TemplatesHandler) ListAgentCategories(w http.ResponseWriter, r *http.Request) {
	h.listCategories(w, "agents")
}

// ListAgentsInCategory lists agent templates in a category.
// GET /api/templates/agents/{category}
func (h *TemplatesHandler) ListAgentsInCategory(w http.ResponseWriter, r *http.Request) {
	h.listInCategory(w, r, "agents")
}

// GetAgentTemplate returns a specific agent template with parsed frontmatter.
// GET /api/templates/agents/{category}/{name}
func (h *TemplatesHandler) GetAgentTemplate(w http.ResponseWriter, r *http.Request) {
	h.getTemplate(w, r, "agents", func(meta map[string]string) map[string]interface{} {
		return map[string]interface{}{
			"tools": metaOrDefault(meta, "tools", ""),
			"model": metaOrDefault(meta, "model", ""),
		}
	})
}

// ListCommandCategories lists command template categories.
// GET /api/templates/commands
func (h *TemplatesHandler) ListCommandCategories(w http.ResponseWriter, r *http.Request) {
	h.listCategories(w, "commands")
}

// ListCommandsInCategory lists command templates in a category.
// GET /api/templates/commands/{category}
func (h *TemplatesHandler) ListCommandsInCategory(w http.ResponseWriter, r *http.Request) {
	h.listInCategory(w, r, "commands")
}

// GetCommandTemplate returns a specific command template with parsed frontmatter.
// GET /api/templates/commands/{category}/{name}
func (h *TemplatesHandler) GetCommandTemplate(w http.ResponseWriter, r *http.Request) {
	h.getTemplate(w, r, "commands", func(meta map[string]string) map[string]interface{} {
		return map[string]interface{}{
			"allowed_tools": metaOrDefault(meta, "allowed-tools", ""),
			"argument_hint": metaOrDefault(meta, "argument-hint", ""),
		}
	})
}

// listCategories answers a kind's category list; a failed fetch is a 200 with the error and no categories.
func (h *TemplatesHandler) listCategories(w http.ResponseWriter, kind string) {
	data, err := h.githubFetch("cli-tool/components/" + kind)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"error":      err.Error(),
			"categories": []interface{}{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"categories": emptyIfNil(extractDirItems(data, "dir"))})
}

// listInCategory answers the templates of one category of a kind, under the kind's own key.
func (h *TemplatesHandler) listInCategory(w http.ResponseWriter, r *http.Request, kind string) {
	category, _, ok := templateSegments(w, r, false)
	if !ok {
		return
	}
	if err := h.known(kind, category, ""); err != nil {
		refuseUnknown(w, err, map[string]interface{}{kind: []interface{}{}})
		return
	}
	data, err := h.githubFetch(fmt.Sprintf("cli-tool/components/%s/%s", kind, category))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"error": err.Error(),
			kind:    []interface{}{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		kind:       emptyIfNil(extractFileItems(data)),
		"category": category,
	})
}

// getTemplate answers one template of a kind with its frontmatter: the name, description, body and
// category every kind has, and the kind's own fields from fields.
func (h *TemplatesHandler) getTemplate(w http.ResponseWriter, r *http.Request, kind string, fields func(meta map[string]string) map[string]interface{}) {
	category, name, ok := templateSegments(w, r, true)
	if !ok {
		return
	}
	filename := name
	if !strings.HasSuffix(filename, ".md") {
		filename += ".md"
	}

	if err := h.known(kind, category, filename); err != nil {
		refuseUnknown(w, err, nil)
		return
	}
	data, err := h.githubFetch(fmt.Sprintf("cli-tool/components/%s/%s/%s", kind, category, filename))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"error": err.Error()})
		return
	}

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		writeJSON(w, http.StatusOK, map[string]interface{}{"error": "unexpected response format"})
		return
	}

	content := decodeGitHubFileContent(dataMap)
	meta, body := parseFrontmatter(content)
	reply := fields(meta)
	reply["name"] = metaOrDefault(meta, "name", name)
	reply["description"] = metaOrDefault(meta, "description", "")
	reply["body"] = body
	reply["category"] = category
	writeJSON(w, http.StatusOK, reply)
}

func metaOrDefault(meta map[string]string, key, fallback string) string {
	if v, ok := meta[key]; ok {
		return v
	}
	return fallback
}
