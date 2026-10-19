package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5063 review (mutants of the patch that no test caught): the in-flight slot and the success reset count a /64 once too, and a RESET (not only a
// change) bumps the replacement count, and the bump comes before the sessions end.

// Mutation: drop rateKey in passwordThrottle.acquire: each address of a network has its own attempt in flight.
func TestPasswordThrottleHoldsOneAttemptInFlightPerIPv6Network(t *testing.T) {
	th := newPasswordThrottle()
	release, ok := th.acquire("2001:db8:1:2::a")
	require.True(t, ok)
	_, other := th.acquire("2001:db8:1:2:ffff::b") // another address of the same /64
	assert.False(t, other, "the /64 already has an attempt in flight")
	release()
	release2, again := th.acquire("2001:db8:1:2:ffff::b")
	assert.True(t, again, "once it is released, the network can try again")
	release2()
}

// Mutation: drop rateKey in passwordThrottle.success: a right password from one address of a network leaves the network's wrong tries counted (or, the
// other way round, clears them for the wrong key).
func TestPasswordThrottleSuccessClearsTheWholeIPv6Network(t *testing.T) {
	th := newPasswordThrottle()
	for i := 0; i < freeFails+2; i++ {
		th.fail("2001:db8:1:2::a")
	}
	require.Positive(t, th.delay("2001:db8:1:2::a"), "the network is waiting")
	th.success("2001:db8:1:2:ffff::b")
	assert.Zero(t, th.delay("2001:db8:1:2::a"), "a sign-in that succeeded from the network cleared its count")
}

// Mutation: drop pwGen.Add in ResetPassword: a password sign-in in flight survives a reset (the same case as the change, through the other route).
func TestSignInInFlightDuringAPasswordResetLeavesNoSession(t *testing.T) {
	g := newPwRig(t)
	g.setPassword("old-password")
	var first atomic.Bool
	gate, resume := make(chan struct{}), make(chan struct{})
	g.ks.SetSessionBackend(&pausingBackend{SessionBackend: g.ks.backend(), onPut: func() {
		if first.CompareAndSwap(false, true) {
			close(gate)
			<-resume
		}
	}})
	var w *httptest.ResponseRecorder
	var out map[string]any
	done := make(chan struct{})
	go func() {
		defer close(done)
		w, out = g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"old-password"}`, "10.0.0.6", nil))
	}()
	<-gate
	g.cli = true // a reset is the command line's
	rw, _ := g.do(g.ar.ResetPassword, g.req("POST", "/api/auth/password/reset", `{"password":"new-password"}`, "127.0.0.1", nil))
	require.Equal(t, http.StatusOK, rw.Code, rw.Body.String())
	close(resume)
	<-done
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "password_changed", out["code"])
	list, err := g.ks.backend().ListSessions(t.Context())
	require.NoError(t, err)
	assert.Empty(t, list, "a reset ended every session and the sign-in in flight left none")
}

// endsSessionsHook runs after DeleteSessionsExcept (the change's "end every session") and, like pausingBackend, holds a session being stored.
type endsSessionsHook struct {
	pausingBackend
	afterDeleteAll func()
}

func (h *endsSessionsHook) DeleteSessionsExcept(ctx context.Context, keep string) (int, error) {
	n, err := h.pausingBackend.SessionBackend.DeleteSessionsExcept(ctx, keep)
	if keep == "" && h.afterDeleteAll != nil {
		h.afterDeleteAll()
	}
	return n, err
}

// The order inside ChangePassword: the replacement count goes up BEFORE the sessions end. A sign-in that matched the old password and stores its
// session right after the sessions ended (this test lets it finish at that moment) must still see the count change.
// Mutation: move pwGen.Add after RevokeAllSessions in ChangePassword: that sign-in sees no replacement, and its session stands beside the new password.
func TestSignInThatStoresItsSessionRightAfterTheSessionsEndedDoesNotStand(t *testing.T) {
	g := newPwRig(t)
	g.setPassword("old-password")
	tok, err := g.ks.NewSession(g.req("GET", "/", "", "10.0.0.5", nil), SessionByLink, false)
	require.NoError(t, err)
	me := &http.Cookie{Name: g.ks.cookieBase, Value: tok}

	gate, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first, once atomic.Bool
	hook := &endsSessionsHook{pausingBackend: pausingBackend{SessionBackend: g.ks.backend(), onPut: func() {
		if first.CompareAndSwap(false, true) { // the sign-in's own session is held
			close(gate)
			<-resume
		}
	}}}
	hook.afterDeleteAll = func() {
		if once.CompareAndSwap(false, true) {
			close(resume) // the sign-in stores its session now: after the sessions ended
			<-done        // and finishes, before the change goes on
		}
	}
	g.ks.SetSessionBackend(hook)
	var w *httptest.ResponseRecorder
	var out map[string]any
	go func() {
		defer close(done)
		w, out = g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"old-password"}`, "10.0.0.6", nil))
	}()
	<-gate
	cw, _ := g.do(g.ar.ChangePassword, g.req("POST", "/api/auth/password/change", `{"current":"old-password","password":"new-password"}`, "10.0.0.5", me))
	require.Equal(t, http.StatusOK, cw.Code, cw.Body.String())
	<-done
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "password_changed", out["code"])
	list, err := g.ks.backend().ListSessions(t.Context())
	require.NoError(t, err)
	assert.Len(t, list, 1, "only the browser that changed the password has a session")
}
