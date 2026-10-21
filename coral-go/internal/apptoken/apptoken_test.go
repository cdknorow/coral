package apptoken

import (
	"regexp"
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
	assert.Equal(t, 1, strings.Count(js, g.token), "the token appears once, in the script only")

	// QA's Q1: each native is captured once, when the script runs; a shim never looks window.__coralNative* up when it is called, so a
	// script that swaps the property afterwards (to catch the token) is not called with it. The same for apply and push.
	assert.Equal(t, 1, strings.Count(js, "window.__coralNativeOpenExternal"), "captured once, in the capture statement")
	assert.Equal(t, 1, strings.Count(js, "window.__coralNativeAppSignIn"))
	assert.Contains(t, js, "var n0 = window.__coralNativeOpenExternal;")
	assert.Contains(t, js, "return call(n0, window, a)")
	assert.Contains(t, js, "var call = Reflect.apply;")
	assert.NotContains(t, js, ".apply(", "no Function.prototype.apply lookup at call time")
	assert.NotContains(t, js, ".push(", "no Array.prototype.push lookup at call time")
	shims := regexp.MustCompile(`function\(\)\{ var a = \[T\];.*?\};`).FindAllString(js, -1)
	require.Len(t, shims, 2, "one shim per native")
	for _, shim := range shims {
		assert.NotContains(t, shim, "__coralNative", "a shim's own source names no native: %s", shim)
		assert.NotContains(t, shim, "window.", "nor any global looked up at call time: %s", shim)
	}
	assert.Less(t, strings.Index(js, "return;"), strings.Index(js, g.token), "the off-origin branch returns before the token")
	assert.NotContains(t, js, "toString")
}
