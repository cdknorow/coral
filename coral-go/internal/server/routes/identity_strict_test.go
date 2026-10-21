package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3418 phase 2: with strict local auth, what the phase-1 report lists as would-break is
// refused: an identity that doesn't match its token (403 identity_mismatch), an agent tool
// without a valid token (401 session_token_required), and anything else from this computer
// without the dashboard's sign-in, a command-line signature or a valid API key (401
// dashboard_auth_required; a page load goes to /auth). Coral Hub keeps its own rules.
func TestStrictLocalAuth(t *testing.T) {
	_, bh := setupBoardTestServer(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	_, err = bh.bs.Subscribe(ctx, "p", "Dev", "Developer", "claude-dev", nil, "")
	require.NoError(t, err)
	token, err := db.MintSessionToken(ctx, "sid-dev", "claude-dev")
	require.NoError(t, err)

	cookie := false
	o := NewIdentityObserver(db, bh.bs, func(*http.Request) bool { return cookie })
	cliCalls := 0
	o.SetVerifiers(
		func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer good-key" },
		func(r *http.Request) (*http.Request, bool) {
			cliCalls++
			if r.Header.Get(cliauth.CLIAuthHeader) == "good" {
				return cliauth.WithVerified(r), true
			}
			return r, false
		})
	o.SetStrict()
	require.True(t, o.Strict())
	var reached []string
	cliVerified := false
	h := o.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method+" "+r.URL.Path)
		cliVerified = cliauth.Verified(r)
	}))
	do := func(method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	code := func(rec *httptest.ResponseRecorder) string {
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		s, _ := out["code"].(string)
		return s
	}
	tok := map[string]string{store.SessionTokenHeader: token, "User-Agent": "coral-board/dev"}

	// An agent's own identity: in. A command-line signature on a tokened request adds
	// nothing: it is never checked, never marks the request, and the handler never sees it.
	withSig := map[string]string{store.SessionTokenHeader: token, "User-Agent": "coral-board/dev", cliauth.CLIAuthHeader: "good"}
	assert.Equal(t, http.StatusOK, do("POST", "/api/board/p/messages", `{"subscriber_id":"Dev","content":"hi"}`, withSig).Code)
	assert.Zero(t, cliCalls)
	assert.False(t, cliVerified)
	rec0 := do("POST", "/api/board/p/tasks", `{"created_by":"Operator","title":"x"}`, withSig)
	assert.Equal(t, http.StatusForbidden, rec0.Code, "still bound to the token's identity")
	reached = reached[:0]
	assert.Equal(t, http.StatusOK, do("POST", "/api/board/p/messages", `{"subscriber_id":"Dev","content":"hi"}`, tok).Code)
	assert.Equal(t, http.StatusOK, do("GET", "/api/board/p/messages?subscriber_id=Dev", ``, tok).Code)

	// As someone else, as the Operator, on another board: refused, and still recorded.
	for _, tc := range []struct{ method, target, body string }{
		{"POST", "/api/board/p/messages", `{"subscriber_id":"Lead","content":"x"}`},
		{"POST", "/api/board/p/tasks", `{"created_by":"Operator","title":"x"}`},
		{"POST", "/api/board/q/messages", `{"subscriber_id":"Dev","content":"x"}`},
		{"GET", "/api/board/p/messages?subscriber_id=Lead", ``},
	} {
		rec := do(tc.method, tc.target, tc.body, tok)
		assert.Equal(t, http.StatusForbidden, rec.Code, tc.target+" "+tc.body)
		assert.Equal(t, "identity_mismatch", code(rec), tc.target+" "+tc.body)
	}
	assert.Len(t, reached, 2, "refused requests never reach a handler")
	rep := o.Report()
	assert.Equal(t, "strict", rep["mode"])
	mm := rep["mismatches"].(map[string]int64)
	assert.EqualValues(t, 2, mm[mismatchImpersonate])
	assert.EqualValues(t, 2, mm[mismatchOperator])
	assert.EqualValues(t, 1, mm[mismatchCrossBoard])

	// An unknown token, or an agent tool with none: 401 session_token_required. An agent
	// tool is never let in by a failed signature either.
	for _, hdr := range []map[string]string{
		{store.SessionTokenHeader: "made-up", "User-Agent": "coral-board/dev"},
		{"User-Agent": "coral-hook-task-sync/dev"},
		{"User-Agent": "coral-board/dev", cliauth.CLIAuthHeader: "bad"},
	} {
		rec := do("GET", "/api/board/p/messages", ``, hdr)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, hdr)
		if hdr[cliauth.CLIAuthHeader] == "" {
			assert.Equal(t, "session_token_required", code(rec), hdr)
		}
	}

	// No credential at all: locked. A credential that is wrong: refused for what it is
	// (in every mode, #3421 v2).
	for hdr, want := range map[*map[string]string]string{
		{}: "dashboard_auth_required",
		{"Origin": "http://127.0.0.1:8420", "Sec-Fetch-Site": "same-origin"}: "dashboard_auth_required",
		{"Authorization": "Bearer wrong"}:                                    "invalid_credential",
		{tunnel.ViaHeader: "1"}:                                              "invalid_credential",
		{cliauth.CLIAuthHeader: "bad"}:                                       "cli_signature_required",
	} {
		rec := do("GET", "/api/sessions/live", ``, *hdr)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, *hdr)
		assert.Equal(t, want, code(rec), *hdr)
	}
	assert.Len(t, reached, 2)

	// Let in: the dashboard's sign-in, a valid API key, the command line's signature (as
	// the local Operator, marked verified for the cloud routes), the real Coral Hub stamp,
	// the liveness check and the command line's hello.
	cookie = true
	assert.Equal(t, http.StatusOK, do("GET", "/api/sessions/live", ``, nil).Code)
	cookie = false
	assert.Equal(t, http.StatusOK, do("GET", "/api/sessions/live", ``, map[string]string{"Authorization": "Bearer good-key"}).Code)
	assert.Equal(t, http.StatusOK, do("POST", "/api/board/p/messages", `{"subscriber_id":"Operator","content":"x"}`, map[string]string{cliauth.CLIAuthHeader: "good", "User-Agent": "coral-board/dev"}).Code)
	assert.True(t, cliVerified, "the handler sees the signature was checked")
	assert.Equal(t, http.StatusOK, do("GET", "/api/board/p/messages", ``, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}).Code)
	assert.Equal(t, http.StatusOK, do("GET", "/api/health", ``, nil).Code)
	assert.Equal(t, http.StatusOK, do("POST", cliauth.CLIHelloPath, `{"nonce":"x"}`, nil).Code)
	assert.False(t, cliVerified)
	assert.Len(t, reached, 8)

	// A page load without the sign-in goes to /auth (the Coral app signs in again from
	// there); the sign-in page and static files are open.
	rec := do("GET", "/", ``, nil)
	assert.Equal(t, http.StatusTemporaryRedirect, rec.Code)
	assert.Equal(t, "/auth?locked=1&return_to=%2F", rec.Header().Get("Location"))
	rec = do("GET", "/some/page?x=1", ``, nil)
	assert.Equal(t, "/auth?locked=1&return_to=%2Fsome%2Fpage%3Fx%3D1", rec.Header().Get("Location"))
	for _, p := range []string{"/auth", "/auth/logout", "/static/app.js", "/manifest.json"} {
		assert.Equal(t, http.StatusOK, do("GET", p, ``, nil).Code, p)
	}
	cookie = true
	assert.Equal(t, http.StatusOK, do("GET", "/", ``, nil).Code)
	assert.Equal(t, http.StatusOK, do("GET", "/", ``, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}).Code)

	b, _ := json.Marshal(o.Report())
	assert.NotContains(t, string(b), token)
	assert.NotContains(t, string(b), "good-key")
}

// The would-break breakdown names the caller, the tool and the route pattern chi matched,
// never the ids in the path, the query or any identity value.
func TestIdentityReportWouldBreakByRoute(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	o := NewIdentityObserver(db, nil, nil)
	r := chi.NewRouter()
	r.Use(o.Middleware)
	r.Get("/api/board/{project}/messages", func(w http.ResponseWriter, r *http.Request) {})
	r.Get("/api/sessions/live", func(w http.ResponseWriter, r *http.Request) {})
	for _, target := range []string{"/api/board/alpha/messages?subscriber_id=Lead", "/api/board/beta/messages", "/api/sessions/live"} {
		req := httptest.NewRequest("GET", target, nil)
		req.Header.Set("User-Agent", "curl/8.4.0")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	req := httptest.NewRequest("GET", "/api/board/alpha/messages", nil)
	req.Header.Set("User-Agent", "coral-hook-task-sync/dev")
	r.ServeHTTP(httptest.NewRecorder(), req)
	got := o.Report()["would_break_by_route"].(map[string]int64)
	assert.Equal(t, map[string]int64{
		"local_other curl GET /api/board/{project}/messages":                    2,
		"local_other curl GET /api/sessions/live":                               1,
		"agent_no_token coral-hook-task-sync GET /api/board/{project}/messages": 1,
	}, got)
	assert.Equal(t, "log_only", o.Report()["mode"])
}
