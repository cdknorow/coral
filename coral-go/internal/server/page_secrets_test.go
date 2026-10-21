package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/pkg/frontend"
)

// tokenMarkup matches the places a credential would be put into a page: a
// CSRF/XSRF name anywhere, a meta tag naming a token, or a hidden input that
// carries a value. (Empty hidden inputs are client-side form state.)
var tokenMarkup = regexp.MustCompile(`(?i)(csrf|xsrf|<meta[^>]+name="[^"]*token[^"]*"|<input[^>]*type="hidden"[^>]*\svalue=)`)

// The dashboard (/) and the popout (/agent/{id}) are served gzipped, the
// popout reflects the agent id from the URL, and / is cached per build. Neither
// may carry a secret or any per-request value (BREACH; one viewer's value
// served to all): two requests with different cookies and credentials must get
// byte-identical pages that contain neither, and no token-carrying markup.
// What the page can contain at all is PageData, whose fields are pinned by
// pkg/frontend TestPageDataFieldsAreReviewed. If a page ever needs a secret, it
// must stop being compressed and cached.
func TestServedPagesCarryNoSecrets(t *testing.T) {
	tmpl, err := frontend.ParseIndex()
	require.NoError(t, err)
	s := &Server{cfg: &config.Config{CoralRoot: "/srv/coral"}, indexTmpl: tmpl}
	const sid = "3f1c2a7e-3b9d-4e2a-9c51-0d8b7a4e2f10"
	creds := []map[string]string{
		{"Cookie": "coral_session=SESSIONSECRETAAAAAAAA; csrf=CSRFSECRETAAAA", "Authorization": "Bearer APIKEYSECRETAAAAAAAA", "X-Api-Key": "APIKEYSECRETAAAAAAAA"},
		{"Cookie": "coral_session=SESSIONSECRETBBBBBBBB; csrf=CSRFSECRETBBBB", "Authorization": "Bearer APIKEYSECRETBBBBBBBB", "X-Api-Key": "APIKEYSECRETBBBBBBBB"},
	}
	pages := map[string]func(hdr map[string]string) *httptest.ResponseRecorder{
		"/": func(hdr map[string]string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			s.serveIndex(rr, req)
			return rr
		},
		"/agent/{server}/{id}": func(hdr map[string]string) *httptest.ResponseRecorder {
			req := withURLParams(httptest.NewRequest(http.MethodGet, "/agent/box-1/"+sid, nil), map[string]string{"sessionID": sid, "server": "box-1"})
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			s.serveAgentPopout(rr, req)
			return rr
		},
	}
	for name, serve := range pages {
		a, b := serve(creds[0]), serve(creds[1])
		require.Equal(t, http.StatusOK, a.Code, name)
		assert.Equal(t, a.Body.String(), b.Body.String(), "%s: the page does not depend on who asks", name)
		for _, c := range creds {
			for _, v := range c {
				for _, secret := range regexp.MustCompile(`[A-Z]+SECRET[A-Z]+`).FindAllString(v, -1) {
					assert.NotContains(t, a.Body.String(), secret, name)
				}
			}
		}
		if loc := tokenMarkup.FindStringIndex(a.Body.String()); loc != nil {
			body := a.Body.String()
			t.Errorf("%s: token-carrying markup: ...%s...", name, body[max(0, loc[0]-60):min(len(body), loc[1]+60)])
		}
		assert.Empty(t, a.Header().Values("Set-Cookie"), "%s sets no cookie", name)
	}
}

// withURLParams attaches chi route parameters, as the router would.
func withURLParams(r *http.Request, params map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}
