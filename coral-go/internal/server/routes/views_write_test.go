package routes

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

func viewsRouter(t *testing.T) (http.Handler, *store.ViewStore) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "coral.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewViewsHandler(db)
	r := chi.NewRouter()
	r.Get("/api/views", h.ListViews)
	r.Get("/api/views/{id}", h.GetView)
	r.Post("/api/views", h.CreateView)
	r.Put("/api/views/{id}", h.UpdateView)
	r.Delete("/api/views/{id}", h.DeleteView)
	return r, store.NewViewStore(db)
}

type reqOpt func(*http.Request)

func do(h http.Handler, method, target, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:8420"
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "application/json")
	for _, o := range opts {
		o(req)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

var (
	asDashboard = func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }
	viewJSON    = `{"name":"v","prompt":"p","html":"<b>hi</b>","scope":"global"}`
)

// Only the positively identified local dashboard may write views: curl, an agent, a proxy, a Bearer holder, the Cloud Hub
// connector, a cross-site page and a non-loopback peer are all refused, for create, update and delete alike.
func TestViewWritesNeedDashboardEvidence(t *testing.T) {
	h, vs := viewsRouter(t)
	id, err := vs.CreateView(t.Context(), &store.CustomView{Name: "keep", HTML: "<b>keep</b>", Scope: "global", CreatedVia: "ui"})
	require.NoError(t, err)
	target := "/api/views/" + viewID(id)
	for name, opt := range map[string]reqOpt{
		"plain curl (loopback, no browser evidence)": func(r *http.Request) {},
		"cross-site browser fetch":                   func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site (another local port)":             func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"none (typed URL / non-fetch)":               func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") },
		"bearer key holder (a proxy)":                func(r *http.Request) { asDashboard(r); r.Header.Set("Authorization", "Bearer k") },
		"cloud hub connector stamp":                  func(r *http.Request) { asDashboard(r); r.Header.Set(tunnel.ViaHeader, "1") },
		"non-loopback peer":                          func(r *http.Request) { asDashboard(r); r.RemoteAddr = "192.0.2.7:4444" },
		"external Host (DNS rebinding)":              func(r *http.Request) { asDashboard(r); r.Host = "evil.example:8420" },
		"foreign Origin, no fetch metadata":          func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
	} {
		for _, tc := range []struct{ method, path, body string }{{"POST", "/api/views", viewJSON}, {"PUT", target, viewJSON}, {"DELETE", target, ""}} {
			w := do(h, tc.method, tc.path, tc.body, opt)
			assert.Equal(t, 403, w.Code, "%s: %s %s", name, tc.method, tc.path)
		}
	}
	v, err := vs.GetView(t.Context(), id)
	require.NoError(t, err)
	require.NotNil(t, v, "no refused request deleted the view")
	assert.Equal(t, "<b>keep</b>", v.HTML, "no refused request changed it")
	list, _ := vs.ListViews(t.Context())
	assert.Len(t, list, 1, "and none created one")
}

func TestDashboardWritesWorkAndRecordTheirProvenance(t *testing.T) {
	h, vs := viewsRouter(t)
	w := do(h, "POST", "/api/views", viewJSON, asDashboard)
	require.Equal(t, 201, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"created_via":"ui"`)
	list, _ := vs.ListViews(t.Context())
	require.Len(t, list, 1)
	assert.Equal(t, "ui", list[0].CreatedVia)
	// Origin-only engines (no fetch metadata) with this very origin are fine too
	w = do(h, "POST", "/api/views", viewJSON, func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:8420") })
	assert.Equal(t, 201, w.Code)
	// a view of unknown provenance becomes "ui" when the user edits it in the dashboard, and is still readable without evidence
	old, err := vs.CreateView(t.Context(), &store.CustomView{Name: "old", HTML: "<i>x</i>", Scope: "global"}) // created_via defaults to api
	require.NoError(t, err)
	got := do(h, "GET", "/api/views/"+viewID(old), "")
	require.Equal(t, 200, got.Code)
	assert.Contains(t, got.Body.String(), `"created_via":"api"`)
	w = do(h, "PUT", "/api/views/"+viewID(old), `{"name":"old","html":"<i>edited</i>","scope":"global"}`, asDashboard)
	require.Equal(t, 200, w.Code)
	v, _ := vs.GetView(t.Context(), old)
	assert.Equal(t, "ui", v.CreatedVia)
	assert.Equal(t, 200, do(h, "DELETE", "/api/views/"+viewID(old), "", asDashboard).Code)
	assert.Equal(t, 200, do(h, "GET", "/api/views", "").Code, "reads need no evidence")
	// a form post is refused even from the dashboard (not a "simple" cross-site request)
	w = do(h, "POST", "/api/views", "name=x", asDashboard, func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") })
	assert.Equal(t, 400, w.Code)
}

// The Cloud Hub can read views (view tier) but never write them: the shared route table says never.
func TestViewWritesStayNeverForTheTunnel(t *testing.T) {
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		assert.Equal(t, tunnel.TierNever, tunnel.Classify(m, "/api/views", ""), m)
		assert.Equal(t, tunnel.TierNever, tunnel.Classify(m, "/api/views/1", ""), m)
	}
	assert.Equal(t, tunnel.TierView, tunnel.Classify("GET", "/api/views", ""))
}

func viewID(i int64) string { return strconv.FormatInt(i, 10) }

// The hub may change the changed-files comparison base, and nothing else about the machine's settings, through this route.
func TestPutGitDiffModeValidatesAndStoresOnlyThatKey(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "coral.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := &SystemHandler{db: db, ss: store.NewSessionStore(db)}
	r := chi.NewRouter()
	r.Put("/api/settings/git-diff-mode", h.PutGitDiffMode)
	for _, mode := range []string{"branch_point", "previous_commit", "main_head"} {
		w := do(r, "PUT", "/api/settings/git-diff-mode", `{"mode":"`+mode+`"}`)
		require.Equal(t, 200, w.Code, mode)
		var v string
		require.NoError(t, db.QueryRow("SELECT value FROM user_settings WHERE key = 'git_diff_mode'").Scan(&v))
		assert.Equal(t, mode, v)
	}
	for _, bad := range []string{`{"mode":"HEAD"}`, `{"mode":""}`, `{"mode":"main_head; DROP TABLE user_settings"}`, `{"mode":["main_head"]}`, `{}`, `[]`, `not json`,
		`{"mode":"main_head","cli_path_claude":"/bin/evil"}`[:0] + `{"mode":"nope","cli_path_claude":"/bin/evil"}`} {
		assert.Equal(t, 400, do(r, "PUT", "/api/settings/git-diff-mode", bad).Code, bad)
	}
	var n int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM user_settings WHERE key <> 'git_diff_mode'").Scan(&n))
	assert.Zero(t, n, "no other setting was written")
	var v string
	require.NoError(t, db.QueryRow("SELECT value FROM user_settings WHERE key = 'git_diff_mode'").Scan(&v))
	assert.Equal(t, "main_head", v, "a refused request changed nothing")
}
