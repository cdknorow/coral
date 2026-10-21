package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
)

// #5636: Coral Hub's owner reads one team's cross-team switches, the lock and who changed them last, for the hub's guided setup. A row of the owner table (owner-only-hub).

func (e *hubSwitchEnv) read(team string, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/board/"+team+"/cross-team/settings", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", team)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	e.h.GetCrossTeamSettingsForHub(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestTheHubOwnerReadsATeamsSwitchStateAndNothingElse(t *testing.T) {
	e := newHubSwitchEnv(t)
	owner := ownerHeaders(ownerLabel)
	rec, out := e.read("team", owner)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"cross_team_artifacts", "cross_team_messaging", "hub_may_change", "last_changed", "linkable"}, keys, "these answers and no others: nothing about any other team")
	assert.Equal(t, false, out["cross_team_messaging"])
	assert.Equal(t, false, out["cross_team_artifacts"])
	assert.Equal(t, true, out["hub_may_change"])
	assert.Equal(t, false, out["linkable"], "a team named by hand has no generated id: it cannot be linked")
	assert.Empty(t, out["last_changed"])
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	// A change by the hub owner shows who and when; a change here shows as this computer; the lock and the switches follow.
	require.Equal(t, 200, e.call("team", "enable", "", owner).Code)
	require.Equal(t, 200, e.call("team", "files/enable", "", owner).Code)
	_, out = e.read("team", owner)
	assert.Equal(t, true, out["cross_team_messaging"])
	assert.Equal(t, true, out["cross_team_artifacts"])
	last, _ := out["last_changed"].(map[string]any)
	msg, _ := last[board.SwitchMessaging].(map[string]any)
	require.NotNil(t, msg)
	assert.Equal(t, "hub", msg["via"])
	assert.Equal(t, ownerLabel, msg["by"])
	require.NoError(t, e.bs.NoteSwitchChangeLocal(context.Background(), "team", board.SwitchMessaging, true, false))
	_, out = e.read("team", owner)
	last, _ = out["last_changed"].(map[string]any)
	msg, _ = last[board.SwitchMessaging].(map[string]any)
	assert.Equal(t, "local", msg["via"])
	assert.Nil(t, msg["by"], "no hub user named on a local change")

	e.lock = false
	_, out = e.read("team", owner)
	assert.Equal(t, false, out["hub_may_change"])
	e.lock, e.lockEr = true, errors.New("settings unreadable")
	_, out = e.read("team", owner)
	assert.Equal(t, false, out["hub_may_change"], "a lock that cannot be read reads as off")
}

// A generated id is linkable; and a team that does not exist is the switch routes' 404.
func TestTheOwnerReadSaysWhetherATeamCanBeLinkedAndNotFound(t *testing.T) {
	e := newHubSwitchEnv(t)
	b, err := e.bs.CreateBoard(context.Background(), "Platform", "Operator")
	require.NoError(t, err)
	_, err = e.bs.Subscribe(context.Background(), b.ID, "Orchestrator", "Orchestrator", "p-orch", nil, "mentions")
	require.NoError(t, err)
	_, out := e.read(b.ID, ownerHeaders(ownerLabel))
	assert.Equal(t, true, out["linkable"])
	rec, out := e.read("nobody", ownerHeaders(ownerLabel))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "no such team", out["error"])
}

// Only the Coral Hub owner: a grantee, a forged stamp, no marker, no hub user, another label, an unreadable record and this computer are all refused with nothing in the answer.
func TestNoOneButTheHubOwnerMayReadItAndThisComputerNot(t *testing.T) {
	e := newHubSwitchEnv(t)
	forged := ownerHeaders(ownerLabel)
	forged["X-Coral-Hub-Via"] = "1"
	noMarker := ownerHeaders(ownerLabel)
	delete(noMarker, "X-Coral-Hub-Role")
	for name, hdr := range map[string]map[string]string{
		"this computer": nil, "a grantee": granteeHeaders(), "no marker": noMarker, "another label": ownerHeaders("hub-user-9999ffff"),
	} {
		rec, out := e.read("team", hdr)
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.Equal(t, "owner_only", out["code"], name)
		assert.NotContains(t, rec.Body.String(), "cross_team_messaging", name)
	}
	withLabel(t, "", errors.New("db closed"))
	rec, _ := e.read("team", ownerHeaders(ownerLabel))
	assert.Equal(t, http.StatusForbidden, rec.Code, "an unreadable enrolment record is no owner")
}
