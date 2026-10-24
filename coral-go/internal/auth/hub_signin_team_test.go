package auth

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #7061: a Coral Hub organisation's machine REFUSES Sign in with Coral Hub: it is not only off, it cannot be turned on, from the settings route or while linking from the sign-in page, and a
// setting that was already on stops counting.
func TestHubSigninIsRefusedOnATeamMachine(t *testing.T) {
	g := newHubRig(t)
	pw := g.passwordSession(t)
	team := false
	g.hs.teamMachine = func() bool { return team }

	// A personal machine: as before.
	require.True(t, g.hs.allowed())
	w, out := g.do(g.hs.Settings, g.req("GET", "/api/auth/hub-signin", "", "10.0.0.5", pw))
	require.Equal(t, 200, w.Code)
	assert.Equal(t, true, out["enabled"])
	assert.Equal(t, false, out["team_machine"])

	// A team's: the setting was on, and no longer counts; the page offers nothing; turning it on is refused and changes nothing.
	team = true
	assert.False(t, g.hs.allowed(), "a setting left on does not let anyone in")
	assert.Empty(t, g.hs.PageState(g.req("GET", "/sign-in", "", "10.0.0.5", nil)), "no Hub button on the sign-in page")
	w, out = g.do(g.hs.Settings, g.req("GET", "/api/auth/hub-signin", "", "10.0.0.5", pw))
	require.Equal(t, 200, w.Code)
	assert.Equal(t, false, out["enabled"], "reported off")
	assert.Equal(t, true, out["team_machine"])
	assert.Equal(t, false, out["offer"], "no offer to turn it on")

	g.set.enabled = false
	w, out = g.do(g.hs.SetSettings, g.req("PUT", "/api/auth/hub-signin", `{"enabled":true}`, "10.0.0.5", pw))
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "hub_signin_team_machine", out["code"])
	assert.False(t, g.set.enabled, "nothing was saved")
	// Turning it off is allowed (it ends nothing that exists, and records the choice).
	w, _ = g.do(g.hs.SetSettings, g.req("PUT", "/api/auth/hub-signin", `{"enabled":false}`, "10.0.0.5", pw))
	assert.Equal(t, 200, w.Code)
}
