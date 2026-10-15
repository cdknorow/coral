package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cliauth"
	"github.com/cdknorow/coral/internal/store"
)

// #3536 on the real router: a board's label in /api/board/<key>/... resolves to its id; an
// exact id always wins; a label two boards share is 409 ambiguous_board with the candidates;
// keys that can't be a URL path id are refused; GET /api/board/<id> describes the board,
// PATCH renames it (only the label); lists carry board_label.
func TestBoardIDsEndToEnd(t *testing.T) {
	srv, ts, _ := strictServer(t, false)
	ctx := context.Background()
	bs := srv.boardStore
	do := func(method, path, body string) (int, map[string]any, []byte) {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out, b
	}
	// A legacy board "sprint" and a new board labelled "launch".
	_, err := bs.Subscribe(ctx, "sprint", "Lead", "Lead", "", nil, nil, "all")
	require.NoError(t, err)
	nb, err := bs.CreateBoard(ctx, "launch", "Operator")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, nb.ID, "Dev", "Dev", "", nil, nil, "all")
	require.NoError(t, err)

	// A post by label lands on the board's id.
	code, out, _ := do("POST", "/api/board/launch/messages", `{"subscriber_id":"Operator","as":"Operator","content":"by label"}`)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, nb.ID, out["project"])
	code, _, raw := do("GET", "/api/board/"+nb.ID+"/messages/all", "")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(raw), "by label")

	// A second board labelled "sprint": the legacy board's id still wins.
	other, err := bs.CreateBoard(ctx, "sprint", "Operator")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, other.ID, "Dev", "Dev", "", nil, nil, "all")
	require.NoError(t, err)
	code, out, _ = do("POST", "/api/board/sprint/messages", `{"subscriber_id":"Operator","as":"Operator","content":"legacy"}`)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, "sprint", out["project"], "an exact id wins over a label")

	// Two boards labelled "launch": ambiguous.
	require.NoError(t, bs.RenameBoard(ctx, other.ID, "launch"))
	code, out, _ = do("GET", "/api/board/launch/messages/all", "")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "ambiguous_board", out["code"])
	cands, _ := out["candidates"].([]any)
	assert.Len(t, cands, 2)

	// Keys that can't be an id in a URL.
	for _, bad := range []string{"%2e%2e", "a%00b", "x%E2%80%AEy"} {
		code, out, _ = do("GET", "/api/board/"+bad+"/messages/all", "")
		assert.Equal(t, http.StatusBadRequest, code, bad)
		assert.Equal(t, "invalid_board_key", out["code"], bad)
	}

	// Board detail and rename (the label only; the id and its rows stay).
	code, out, _ = do("GET", "/api/board/"+nb.ID, "")
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, "launch", out["board_label"])
	assert.EqualValues(t, 2, out["members"], "Dev, and the Operator its post subscribed")
	code, out, _ = do("PATCH", "/api/board/"+nb.ID, `{"label":"Launch Team"}`)
	assert.Equal(t, http.StatusBadRequest, code, "a new label follows the name rule: %v", out)
	// QA's rule: a label can't look like an id (a legacy board's name is fine: below).
	for _, label := range []string{other.ID, "b-aaaaaaaaaa"} {
		code, out, _ = do("PATCH", "/api/board/"+nb.ID, `{"label":"`+label+`"}`)
		assert.Equal(t, http.StatusBadRequest, code, "label %q: %v", label, out)
		assert.Equal(t, "invalid_board_name", out["code"], label)
	}
	code, out, _ = do("PATCH", "/api/board/sprint", `{"label":"sprint"}`)
	assert.Equal(t, http.StatusOK, code, "a legacy board may keep its own name: %v", out)
	code, out, _ = do("PATCH", "/api/board/"+nb.ID, `{"label":"sprint"}`)
	assert.Equal(t, http.StatusOK, code, "a label equal to a legacy board's name is a shared label: %v", out)
	code, out, _ = do("POST", "/api/board/sprint/messages", `{"subscriber_id":"Operator","as":"Operator","content":"still legacy"}`)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, "sprint", out["project"], "the legacy id still wins over the shared label")
	code, out, _ = do("PATCH", "/api/board/"+nb.ID, `{"label":"launch-team"}`)
	require.Equal(t, http.StatusOK, code, out)
	code, _, raw = do("GET", "/api/board/launch-team/messages/all", "")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(raw), "by label", "the renamed board keeps its messages")
	code, _, _ = do("GET", "/api/board/"+url.PathEscape("no-such-board"), "")
	assert.Equal(t, http.StatusNotFound, code)

	// Lists carry board_label.
	var projects []map[string]any
	code, _, raw = do("GET", "/api/board/projects", "")
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, json.Unmarshal(raw, &projects))
	labels := map[string]any{}
	for _, p := range projects {
		labels[p["project"].(string)] = p["board_label"]
	}
	assert.Equal(t, "launch-team", labels[nb.ID])
	assert.Equal(t, "sprint", labels["sprint"])

	// A team's row is keyed by its board id; the team list and detail carry the label.
	_, err = store.NewTeamStore(srv.db).CreateTeam(ctx, &store.Team{Name: nb.ID})
	require.NoError(t, err)
	code, out, _ = do("GET", "/api/teams/all", "")
	require.Equal(t, http.StatusOK, code)
	teams, _ := out["teams"].([]any)
	require.Len(t, teams, 1)
	assert.Equal(t, "launch-team", teams[0].(map[string]any)["board_label"])
	code, out, _ = do("GET", "/api/teams/detail/"+nb.ID, "")
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, "launch-team", out["board_label"])
	assert.Equal(t, nb.ID, out["name"])
}

// Security's #3536 SHOULDs, strict mode on the real router. (1) The identity check runs on
// the RESOLVED board id and compares exactly: an agent on board b-x (label "team") may use it
// by label or id; another board by id or label, and a case variant of its own id, are
// refused. (2) new_board for a taken name is the Operator's: an agent's launch is refused,
// the Operator's (the signed command line) starts the second board.
func TestBoardIDsStrictAgentChecks(t *testing.T) {
	srv, ts, dir := strictServer(t, true)
	ctx := context.Background()
	bs := srv.boardStore
	mine, err := bs.CreateBoard(ctx, "team", "Operator")
	require.NoError(t, err)
	other, err := bs.CreateBoard(ctx, "other-team", "Operator")
	require.NoError(t, err)
	sid := "eeeeeeee-3536-0000-0000-00000000000e"
	require.NoError(t, store.NewSessionStore(srv.db).RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "dev", WorkingDir: t.TempDir()}))
	tok, err := srv.db.MintSessionToken(ctx, sid, "claude-"+sid)
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, mine.ID, "Dev", "Dev", "claude-"+sid, nil, nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, other.ID, "QA", "QA", "", nil, nil, "all")
	require.NoError(t, err)

	asAgent := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.SessionTokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	post := `{"subscriber_id":"Dev","content":"hi"}`
	for _, key := range []string{"team", mine.ID} {
		code, out := asAgent("POST", "/api/board/"+key+"/messages", post)
		assert.Equal(t, http.StatusOK, code, "own board by %q: %v", key, out)
	}
	for _, key := range []string{other.ID, "other-team", strings.ToUpper(mine.ID), "Team"} {
		code, out := asAgent("POST", "/api/board/"+key+"/messages", post)
		assert.Equal(t, http.StatusForbidden, code, "%q: %v", key, out)
		assert.Equal(t, "identity_mismatch", out["code"], "%q", key)
	}

	// (2) new_board for a taken name.
	launch := `{"working_dir":"` + t.TempDir() + `","agent_type":"terminal","board_name":"team","new_board":true}`
	code, out := asAgent("POST", "/api/sessions/launch", launch)
	assert.Equal(t, http.StatusForbidden, code, out)
	assert.Equal(t, "forbidden_for_agent", out["code"])
	named, err := bs.BoardsLabelled(ctx, "team")
	require.NoError(t, err)
	assert.Len(t, named, 1, "the agent's launch started nothing")

	cliTok, err := cliauth.ReadToken(dir)
	require.NoError(t, err)
	resp, err := cliauth.CLIClient{Base: ts.URL, Token: cliTok}.Do(ctx, "POST", "/api/sessions/launch", []byte(launch), nil, 0)
	require.NoError(t, err)
	var opOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&opOut)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the Operator may: %v", opOut)
	ids, _ := bs.BoardsLabelledForLaunch(ctx, "team")
	assert.Len(t, ids, 2, "the Operator's launch started a second 'team'")
}

// Default mode: an agent's new_board for a taken name goes through and is recorded in the
// identity report's agent_operator_calls.
func TestAgentNewBoardRecordedInDefaultMode(t *testing.T) {
	srv, ts, _ := strictServer(t, false)
	ctx := context.Background()
	b, err := srv.boardStore.CreateBoard(ctx, "team", "Operator")
	require.NoError(t, err)
	_, err = srv.boardStore.Subscribe(ctx, b.ID, "Lead", "Lead", "", nil, nil, "all")
	require.NoError(t, err)
	sid := "ffffffff-3536-0000-0000-00000000000f"
	require.NoError(t, store.NewSessionStore(srv.db).RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "dev", WorkingDir: t.TempDir()}))
	tok, err := srv.db.MintSessionToken(ctx, sid, "claude-"+sid)
	require.NoError(t, err)
	req, _ := http.NewRequest("POST", ts.URL+"/api/sessions/launch", strings.NewReader(`{"working_dir":"`+t.TempDir()+`","agent_type":"terminal","board_name":"team","new_board":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(store.SessionTokenHeader, tok)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	calls := srv.identity.Report()["agent_operator_calls"].(map[string]int64)
	assert.EqualValues(t, 1, calls["claude-"+sid+" POST /api/sessions/launch new_board"], calls)
}
