package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3869 (Security's #3867 finding): outside strict mode a cross-board request by an agent was
// only logged, so an agent read another team's board by name. In every mode an agent's token
// now acts only on boards its session is subscribed on, apart from joining a board and a
// cross-team send (whose handler checks the sender). Each allowed path, and each refusal.
func TestAgentActsOnlyOnItsOwnBoards(t *testing.T) {
	_, bh := setupBoardTestServer(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	mint := func(sid, session string) map[string]string {
		token, err := db.MintSessionToken(ctx, sid, session)
		require.NoError(t, err)
		return map[string]string{store.SessionTokenHeader: token, "User-Agent": "coral-board/dev"}
	}
	subscribe := func(board, role, session string) {
		_, err := bh.bs.Subscribe(ctx, board, role, role, session, nil, "")
		require.NoError(t, err)
	}
	subscribe("team-a", "Orchestrator", "claude-a-orch")
	subscribe("team-a", "Security Reviewer", "claude-a-sec")
	subscribe("team-b", "Security Reviewer", "claude-b-sec")
	subscribe("team-a", "Dev", "claude-two") // on two boards, with a different role on each
	subscribe("team-c", "Lead", "claude-two")
	_, err = bh.bs.PostMessage(ctx, "team-b", "Security Reviewer", "team-b's own business", nil)
	require.NoError(t, err)
	aOrch, aSec, two := mint("sid-a-orch", "claude-a-orch"), mint("sid-a-sec", "claude-a-sec"), mint("sid-two", "claude-two")
	loner := mint("sid-loner", "claude-loner") // a token, never joined

	o := NewIdentityObserver(db, bh.bs, func(*http.Request) bool { return false })
	reached := []string{}
	h := o.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Method+" "+r.URL.Path)
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

	allowed := []struct {
		name, method, target, body string
		hdr                        map[string]string
	}{
		{"its own board", "GET", "/api/board/team-a/messages/all", "", aSec},
		{"a cross-team send to another board", "POST", "/api/board/team-b/external-messages",
			`{"from_board":"team-a","subscriber_id":"Orchestrator","session_name":"claude-a-orch","content":"hello"}`, aOrch},
		{"joining another board", "POST", "/api/board/team-d/subscribe", `{"subscriber_id":"Security Reviewer","session_name":"claude-a-sec"}`, aSec},
		{"an agent never on a board joins one", "POST", "/api/board/team-d/subscribe", `{"subscriber_id":"New","session_name":"claude-loner"}`, loner},
		{"one of its two boards, as its role there", "GET", "/api/board/team-c/messages?subscriber_id=Lead", "", two},
		{"the other of its two boards, as its role there", "POST", "/api/board/team-a/messages", `{"subscriber_id":"Dev","content":"x"}`, two},
		{"the board list", "GET", "/api/board/projects", "", aSec},
	}
	for _, tc := range allowed {
		rec := do(tc.method, tc.target, tc.body, tc.hdr)
		assert.NotEqual(t, http.StatusForbidden, rec.Code, "%s: %s", tc.name, rec.Body.String())
	}

	refused := []struct {
		name, method, target, body string
		hdr                        map[string]string
	}{
		{"another team's messages", "GET", "/api/board/team-b/messages/all", "", aSec},
		{"another team's messages by id", "GET", "/api/board/team-b/messages/all?id=1", "", aSec},
		{"another team's unread, as its namesake", "GET", "/api/board/team-b/messages?subscriber_id=Security+Reviewer&all=true", "", aSec},
		{"another team's status", "GET", "/api/board/team-b/status", "", aOrch},
		{"another team's working mode", "PUT", "/api/board/team-b/working-mode", `{"mode":"x"}`, aOrch},
		{"waiting on another team's board", "POST", "/api/board/team-b/waits", `{"subscriber_id":"Security Reviewer"}`, aSec},
		{"posting on another team's board", "POST", "/api/board/team-b/messages", `{"subscriber_id":"Security Reviewer","content":"x"}`, aSec},
		{"an agent never on a board reads one", "GET", "/api/board/team-b/messages/all", "", loner},
		{"a cross-team send is a POST", "GET", "/api/board/team-b/external-messages", "", aOrch},
	}
	before := len(reached)
	for _, tc := range refused {
		rec := do(tc.method, tc.target, tc.body, tc.hdr)
		assert.Equal(t, http.StatusForbidden, rec.Code, tc.name)
		assert.Equal(t, "identity_mismatch", code(rec), tc.name)
		assert.NotContains(t, rec.Body.String(), "own business", tc.name)
	}
	assert.Len(t, reached, before, "a refused request never reaches a handler")

	// The Operator (no token) still reads any board.
	rec := do("GET", "/api/board/team-b/messages/all", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "GET /api/board/team-b/messages/all", reached[len(reached)-1])
}
