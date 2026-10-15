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
