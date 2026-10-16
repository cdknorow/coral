package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4032 (Security's #4031 finding 2), the census on the real router: every GET under /api and
// /ws, sent with a signed-in cookie from another page of the same site (another localhost port),
// is refused as foreign_origin before its handler; the same request from Coral's own page is
// not; /api answers say Cross-Origin-Resource-Policy: same-origin and nosniff.
func TestEveryAPIReadRefusesAnotherPage(t *testing.T) {
	cfg := newE2EConfig(t, t.TempDir())
	cfg.HubMode = true // every route
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := newTestServer(t, cfg, db, stack.backend, stack.terminal)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	token, err := srv.keyStore.CreateSession("", "test")
	require.NoError(t, err)
	probe := httptest.NewRequest("GET", ts.URL+"/", nil)
	cookie := &http.Cookie{Name: srv.keyStore.SessionCookieName(probe), Value: token}

	param := regexp.MustCompile(`\{[^}]*\}|\*`)
	n := 0
	require.NoError(t, chi.Walk(srv.Router(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != http.MethodGet || !(strings.HasPrefix(route, "/api/") || strings.HasPrefix(route, "/ws")) {
			return nil
		}
		n++
		for _, site := range []string{"same-site", "same-origin"} {
			req, _ := http.NewRequest("GET", ts.URL+param.ReplaceAllString(route, "x1"), nil)
			req.AddCookie(cookie)
			req.Header.Set("Sec-Fetch-Site", site)
			if site == "same-site" {
				req.Header.Set("Origin", "http://localhost:3000")
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			foreign := resp.StatusCode == http.StatusForbidden && strings.Contains(string(b), "foreign_origin")
			if site == "same-site" {
				assert.True(t, foreign, "%s from another page: %d %s", route, resp.StatusCode, b)
			} else {
				assert.False(t, foreign, "%s from Coral's own page was refused", route)
			}
			if strings.HasPrefix(route, "/api/") {
				assert.Equal(t, "same-origin", resp.Header.Get("Cross-Origin-Resource-Policy"), route)
				assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"), route)
			}
		}
		return nil
	}))
	assert.Greater(t, n, 150, "the walk found the GET routes")
}
