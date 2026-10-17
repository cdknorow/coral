package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cliauth"
	"github.com/cdknorow/coral/internal/store"
)

// #4290 (Security's SHOULD 3 in #4288), the real router with strict local auth OFF: reading or
// rotating the API key needs a verified Operator (the key itself, the signed command line, a
// dashboard sign-in), not just a loopback peer. A bare local request, and an agent's token,
// are refused.
func TestAPIKeyRoutesNeedAVerifiedOperatorWithStrictOff(t *testing.T) {
	srv, ts, dir := strictServer(t, false)
	ctx := context.Background()
	key := srv.keyStore.Key()
	agentTok, err := srv.db.MintSessionToken(ctx, "sid-dev", "claude-dev")
	require.NoError(t, err)
	cliTok, err := cliauth.ReadToken(dir)
	require.NoError(t, err)
	cli := cliauth.CLIClient{Base: ts.URL, Token: cliTok}

	do := func(method, path, body string, set func(*http.Request)) (int, string) {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if set != nil {
			set(req)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Sign in the way the dashboard does and keep the session cookie.
	req, _ := http.NewRequest("POST", ts.URL+"/auth/key", strings.NewReader(`{"key":"`+key+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cookies := resp.Cookies()
	require.NotEmpty(t, cookies, "the sign-in sets a session cookie")

	for _, path := range []string{"/api/system/api-key", "/api/system/api-key/regenerate"} {
		method := "GET"
		if strings.HasSuffix(path, "regenerate") {
			method = "POST"
		}
		for name, set := range map[string]func(*http.Request){
			"no credential": nil,
			"an agent token": func(r *http.Request) {
				r.Header.Set(store.SessionTokenHeader, agentTok)
			},
		} {
			code, body := do(method, path, "", set)
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, code, "%s %s with %s: %s", method, path, name, body)
			assert.NotContains(t, body, key, "%s with %s: no key in the refusal", path, name)
		}
	}
	assert.Equal(t, key, srv.keyStore.Key(), "a refused rotation changed nothing")

	// The Operator's credentials read it.
	code, body := do("GET", "/api/system/api-key", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) })
	assert.Equal(t, http.StatusOK, code, "the API key: %s", body)
	code, body = do("GET", "/api/system/api-key", "", func(r *http.Request) {
		for _, c := range cookies {
			r.AddCookie(c)
		}
	})
	assert.Equal(t, http.StatusOK, code, "the dashboard cookie: %s", body)
	assert.Contains(t, body, key)
	resp2, err := cli.Do(ctx, "GET", "/api/system/api-key", nil, nil, 0)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode, "the signed command line")
}
