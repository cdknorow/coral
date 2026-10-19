package auth

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mutation: drop the sameOriginWrite check of Link: a page on another site links this computer to a Coral Hub account.
func TestHubLinkRefusesAForeignOrigin(t *testing.T) {
	g := newHubRig(t)
	g.hub.linked = false
	g.set.enabled = false
	w, out := g.do(g.hs.Link, foreign(g.req("POST", "/auth/hub/link", `{"setup_code":"WRONG-CODE"}`, "10.0.0.5", nil)))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "cross_origin", out["code"], "refused for its origin, before the setup code is even looked at")
	assert.False(t, g.hub.linked)
}

// Mutation: drop the same-origin check of hubSettingsCaller: a page on another site rewrites the Hub return origins of a signed-in Operator.
func TestHubOriginsWriteRefusesAForeignOrigin(t *testing.T) {
	g := newHubRig(t)
	pw := g.passwordSession(t)
	before := append([]string(nil), g.set.origins...)
	w, out := g.do(g.hs.SetOrigins, foreign(g.req("PUT", "/api/auth/hub-origins", `{"origins":["https://evil.example"]}`, "10.0.0.5", pw)))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "cross_origin", out["code"])
	assert.Equal(t, before, g.set.origins, "the return origins are unchanged")
}

// Mutation: drop or raise the count check of SetOrigins: the list the hub is told grows without bound.
func TestHubOriginsOverTheLimitAreRefusedAndNothingIsStored(t *testing.T) {
	g := newHubRig(t)
	pw := g.passwordSession(t)
	list := func(n int) string {
		var s []string
		for i := 0; i < n; i++ {
			s = append(s, fmt.Sprintf(`"https://o%d.example"`, i))
		}
		return `{"origins":[` + strings.Join(s, ",") + `]}`
	}
	room := maxHubOrigins - len(g.hs.loopbackOrigins()) // the server's own loopback origins count against the cap

	w, out := g.do(g.hs.SetOrigins, g.req("PUT", "/api/auth/hub-origins", list(room+1), "10.0.0.5", pw))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "too_many", out["code"])
	assert.Empty(t, g.set.origins, "a refused list stores nothing")

	w, _ = g.do(g.hs.SetOrigins, g.req("PUT", "/api/auth/hub-origins", list(room), "10.0.0.5", pw))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Len(t, g.set.origins, room, "a list exactly at the limit is accepted")
}
