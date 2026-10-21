package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/store"
)

// #4233 end to end, the real router: a claimed name spelled with another capitalisation (or
// followed by trailing data) was invisible to every guard in front of the board routes and still
// decoded by the handler. With an agent's token each of these is refused by the guard that owns
// it, nothing is written as another member or the Operator, and a legitimate request spelled the
// same odd way is attributed to its own session.
func TestBoardClaimsInOtherSpellingsAreCheckedLikeTheExactOne(t *testing.T) {
	for _, strict := range []bool{true, false} {
		srv, ts, dir := strictServer(t, strict)
		ctx := context.Background()
		devTok, err := srv.db.MintSessionToken(ctx, "sid-dev", "claude-dev")
		require.NoError(t, err)
		cliTok, err := cliauth.ReadToken(dir)
		require.NoError(t, err)
		cli := cliauth.CLIClient{Base: ts.URL, Token: cliTok}
		asCLI := func(method, path, body string) (int, string) {
			resp, err := cli.Do(ctx, method, path, []byte(body), nil, 0)
			require.NoError(t, err)
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		send := func(token, ua, method, path, body string) (int, string) {
			req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if token != "" {
				req.Header.Set(store.SessionTokenHeader, token)
			}
			if ua != "" {
				req.Header.Set("User-Agent", ua)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		code, body := asCLI("POST", "/api/board/team/subscribe", `{"subscriber_id":"Dev","session_name":"claude-dev","job_title":"Developer"}`)
		require.Equal(t, http.StatusOK, code, body)
		code, body = asCLI("POST", "/api/board/team/subscribe", `{"subscriber_id":"Lead","session_name":"claude-lead","job_title":"Lead"}`)
		require.Equal(t, http.StatusOK, code, body)
		agent := func(method, path, b string) (int, string) { return send(devTok, "coral-board/test", method, path, b) }

		// Another member, the Operator and a trailing-data claim, in every spelling.
		refused := []struct{ path, body, code string }{
			{"/api/board/team/messages", `{"subscriber_id":"Lead","content":"MARK1"}`, "identity_mismatch"},
			{"/api/board/team/messages", `{"Subscriber_ID":"Lead","content":"MARK2"}`, "identity_mismatch"},
			{"/api/board/team/messages", `{"SUBSCRIBER_ID":"Lead","content":"MARK3"}`, "identity_mismatch"},
			{"/api/board/team/messages", `{"subscriber_id":"Dev","SUBSCRIBER_ID":"Lead","content":"MARK4"}`, "identity_mismatch"},
			{"/api/board/team/messages", `{"subscriber_id":"Lead","content":"MARK5"} junk`, "identity_mismatch"},
			{"/api/board/team/messages", `{"SUBSCRIBER_ID":"Lead","content":"MARK6"}`, "identity_mismatch"},
			{"/api/board/team/tasks", `{"CREATED_BY":"Operator","title":"MARK7"}`, "identity_mismatch"},
			{"/api/board/team/tasks", `{"Created_By":"Lead","title":"MARK8"}`, "identity_mismatch"},
			{"/api/board/team/messages", `{"SUBSCRIBER_ID":"Operator","content":"MARK9"}`, "identity_mismatch"},
		}
		for _, c := range refused {
			code, body := agent("POST", c.path, c.body)
			assert.Equal(t, http.StatusForbidden, code, "strict=%v %s: %s", strict, c.body, body)
			assert.Equal(t, c.code, codeOfBody([]byte(body)), "strict=%v %s", strict, c.body)
		}
		// Control characters in a name, in another spelling: refused before anything else reads it.
		code, body = agent("POST", "/api/board/team/messages", `{"Subscriber_ID":"a\nb","content":"MARK10"}`)
		assert.Equal(t, http.StatusBadRequest, code, "strict=%v: %s", strict, body)
		// The Operator's name from an agent's command line with no token, in another spelling.
		if !strict {
			code, body = send("", "coral-board/test", "POST", "/api/board/team/messages", `{"SUBSCRIBER_ID":"Operator","content":"MARK11"}`)
			assert.Equal(t, http.StatusBadRequest, code, "no-token agent command line: %s", body)
			assert.Contains(t, body, "reserved for the Operator")
		}
		// A legitimate request in an odd spelling is its own session's, and is attributed so.
		code, body = agent("POST", "/api/board/team/messages", `{"SUBSCRIBER_ID":"Dev","content":"LEGIT-OK"}`)
		assert.Equal(t, http.StatusOK, code, "strict=%v: %s", strict, body)

		code, body = asCLI("GET", "/api/board/team/messages/all", "")
		require.Equal(t, http.StatusOK, code, body)
		for _, m := range []string{"MARK1", "MARK2", "MARK3", "MARK4", "MARK5", "MARK6", "MARK7", "MARK8", "MARK9", "MARK10", "MARK11"} {
			assert.NotContains(t, body, m, "strict=%v: nothing a refused request carried was written", strict)
		}
		assert.Contains(t, body, "LEGIT-OK")
		code, body = asCLI("GET", "/api/board/team/tasks", "")
		require.Equal(t, http.StatusOK, code, body)
		assert.NotContains(t, body, "MARK7")
		assert.NotContains(t, body, "MARK8")
	}
}
