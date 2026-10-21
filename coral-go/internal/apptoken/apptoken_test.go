package apptoken

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #78 W1: a native binding does nothing for a call without this launch's token.
func TestOnlyTheLaunchTokenIsAllowed(t *testing.T) {
	g, err := New()
	require.NoError(t, err)
	assert.True(t, g.Allowed(g.token))
	for _, bad := range []string{"", "x", g.token + "x", g.token[:len(g.token)-1], strings.ToUpper(g.token), "0000000000000000000000000000000000000000000000000000000000000000"} {
		assert.False(t, g.Allowed(bad), "%q", bad)
	}
	var nilGuard *Guard
	assert.False(t, nilGuard.Allowed(""), "no guard allows nothing")
	assert.False(t, (&Guard{}).Allowed(""), "an empty token is never a match")
}

func TestEachLaunchHasItsOwnToken(t *testing.T) {
	a, err := New()
	require.NoError(t, err)
	b, err := New()
	require.NoError(t, err)
	assert.Len(t, a.token, 64)
	assert.NotEqual(t, a.token, b.token)
	assert.False(t, a.Allowed(b.token))
}

// The shim script installs the page-facing functions only on the dashboard origin, with the token in a closure; any
// other origin gets them deleted and returns before the token appears.
func TestShimScriptInstallsOnlyOnTheDashboardOrigin(t *testing.T) {
	g, err := New()
	require.NoError(t, err)
	js := g.ShimScript("http://localhost:8420", []Native{
		{Page: "_coralOpenExternal", Native: "__coralNativeOpenExternal"},
		{Page: "_coralAppSignIn", Native: "__coralNativeAppSignIn"},
	})
	assert.Contains(t, js, `location.origin !== "http://localhost:8420"`)
	assert.Contains(t, js, "delete window._coralOpenExternal")
	assert.Contains(t, js, "delete window._coralAppSignIn")
	assert.Contains(t, js, "window.__coralNativeOpenExternal.apply(window, a)")
	assert.Equal(t, 1, strings.Count(js, g.token), "the token appears once, in the script only")
	assert.Less(t, strings.Index(js, "return;"), strings.Index(js, g.token), "the off-origin branch returns before the token")
	assert.NotContains(t, js, "toString")
}
