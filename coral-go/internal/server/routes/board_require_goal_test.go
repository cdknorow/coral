package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
)

// #7116: the per-board require-a-goal setting. Only this computer changes it, on or off (a tunnelled PUT, the owner's included, is 403 local_only and changes nothing); the change is logged; and a task with no
// goal on a board that requires one is a 400 goal_required with nothing stored.
func TestRequireGoalIsChangedOnlyOnThisComputer(t *testing.T) {
	_, h := setupBoardTestServer(t)
	withLabel(t, ownerLabel, nil)
	ctx := context.Background()

	code, out := boardCall(t, h, http.MethodGet, "", nil)
	require.Equal(t, 200, code)
	assert.Equal(t, false, out["require_goal"], "off by default")

	for _, field := range []string{`"require_goal": true`, `"require_goal": false`, `"require_goal": true, "health": {"on": false}`} {
		for name, hdr := range map[string]map[string]string{"the owner through the hub": ownerHeaders(ownerLabel), "a grantee": granteeHeaders()} {
			code, out = boardCall(t, h, http.MethodPut, `{"subscriber_id":"Operator",`+field+`}`, hdr)
			assert.Equal(t, 403, code, name+" "+field)
			assert.Contains(t, []any{"local_only", "owner_only"}, out["code"], name+" "+field)
		}
	}
	got, err := h.bs.GetBoardSettings(ctx, "team")
	require.NoError(t, err)
	assert.False(t, got.RequireGoal, "nothing of a refused request is saved")

	// Hub owner, with the owner marker: local_only by name for this setting.
	code, out = boardCall(t, h, http.MethodPut, `{"subscriber_id":"Operator","require_goal":true}`, ownerHeaders(ownerLabel))
	assert.Equal(t, 403, code)
	assert.Equal(t, "local_only", out["code"])

	// This computer: on, then logged, then off.
	code, out = boardCall(t, h, http.MethodPut, `{"subscriber_id":"Operator","require_goal":true}`, nil)
	require.Equal(t, 200, code, "%v", out)
	assert.Equal(t, true, out["require_goal"])
	log, err := h.bs.ListLog(ctx, "team")
	require.NoError(t, err)
	require.Len(t, log, 1)
	assert.Equal(t, "require_goal", log[0].Field)
	assert.Equal(t, "false", log[0].Old)
	assert.Equal(t, "true", log[0].New)
	code, _ = boardCall(t, h, http.MethodPut, `{"subscriber_id":"Operator","require_goal":true}`, nil)
	require.Equal(t, 200, code)
	log, _ = h.bs.ListLog(ctx, "team")
	assert.Len(t, log, 1, "a save that changes nothing is not a log row")
	code, out = boardCall(t, h, http.MethodPut, `{"subscriber_id":"Operator","require_goal":false}`, nil)
	require.Equal(t, 200, code)
	assert.Equal(t, false, out["require_goal"])

	// An agent cannot change it, whatever it claims.
	code, _ = boardCall(t, h, http.MethodPut, `{"subscriber_id":"Dev","require_goal":true}`, nil)
	assert.Equal(t, 403, code)
}

func TestATaskWithNoGoalOnARequireGoalBoardIs400GoalRequired(t *testing.T) {
	srv, h := setupBoardTestServer(t)
	ctx := context.Background()
	on := true
	_, err := h.bs.UpdateBoardSettings(ctx, "team", board.BoardSettingsUpdate{RequireGoal: &on})
	require.NoError(t, err)
	post := func(body string) (int, map[string]any) {
		resp, err := http.Post(srv.URL+"/api/board/team/tasks", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, out := post(`{"title":"no goal","subscriber_id":"Operator"}`)
	assert.Equal(t, 400, code, "%v", out)
	assert.Equal(t, "goal_required", out["code"])
	assert.Equal(t, "this board requires a goal for every task: create or pick one with coral-board goal", out["error"])
	tasks, err := h.bs.ListTasks(ctx, "team")
	require.NoError(t, err)
	assert.Empty(t, tasks, "nothing was stored")
}
