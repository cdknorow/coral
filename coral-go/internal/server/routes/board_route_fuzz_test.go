package routes

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// FuzzBoardRoute (#5002): the gates in front of the router read a request's board and its tail from the URL before chi has routed it. The
// router is the oracle: for every path chi sends to /api/board/{project}/* (or /api/board/{project}), boardRoute reads the same project (decoded,
// as urlParam does) and the same tail, and routedPath is the raw path when the request has one. A project holds a "/" only when the path wrote it
// as %2F, so an encoded slash never splits a board name. It never panics, on any path.
// go test -fuzz FuzzBoardRoute -fuzztime 10m ./internal/server/routes/ fuzzes.
func FuzzBoardRoute(f *testing.F) {
	for _, p := range []string{"/api/board/team/messages", "/api/board/a%2Fb/messages", "/api/board/a%2fb", "/api/board//x", "/api/board/", "/api/board",
		"/api/board/%zz/x", "/api/board/p/%2e%2e/x", "/api/board/p/a/b/c", "/api/board/%E2%82%AC/x", "/api/boards/x", "/x/api/board/y", "/api/board/p%00/x", "/api/board/a%252Fb/x", "/api/board/p?x=1", "/api/board/p/tasks/3/complete", "//api/board/p"} {
		f.Add(p)
	}
	var seen struct{ project, tail string }
	var matched bool
	router := chi.NewRouter()
	record := func(w http.ResponseWriter, r *http.Request) {
		matched = true
		seen.project, seen.tail = chi.URLParam(r, "project"), chi.URLParam(r, "*")
	}
	router.HandleFunc("/api/board/{project}/*", record)
	router.HandleFunc("/api/board/{project}", record)
	f.Fuzz(func(t *testing.T, p string) {
		u, err := url.ParseRequestURI(p)
		if err != nil || !strings.HasPrefix(p, "/") {
			t.Skip()
		}
		req := &http.Request{Method: http.MethodGet, URL: u, Header: http.Header{}}
		routed := routedPath(req)
		if u.RawPath != "" && routed != u.RawPath || u.RawPath == "" && routed != u.Path {
			t.Fatalf("routedPath %q for path %q raw %q", routed, u.Path, u.RawPath)
		}
		project, tail, ok := boardRoute(req)
		if ok != strings.HasPrefix(routed, "/api/board/") {
			t.Fatalf("ok %v for %q", ok, routed)
		}
		if strings.Contains(project, "/") && !strings.Contains(strings.ToLower(routed), "%2f") {
			t.Fatalf("project %q has a slash that %q did not write as %%2F", project, routed)
		}
		matched = false
		router.ServeHTTP(httptest.NewRecorder(), req)
		if !matched {
			return
		}
		want, derr := url.PathUnescape(seen.project)
		if derr != nil {
			want = seen.project
		}
		if !ok || project != want || tail != seen.tail {
			t.Fatalf("path %q (raw %q): chi routes project %q tail %q, boardRoute reads ok=%v project %q tail %q", u.Path, u.RawPath, seen.project, seen.tail, ok, project, tail)
		}
	})
}
