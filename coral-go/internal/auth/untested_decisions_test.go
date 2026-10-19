package auth

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5021: decisions in the auth routes no test reached (found by statement coverage): the cross-origin refusals of the session and password
// writes, the failure of a password write (fail closed: an error answer, nothing changed, no session ended), the size limits of a new
// password and of the initial-password file, and the answers a password-set Coral gives to setup. Each has a mutation check in its comment:
// flip the decision and the test fails.

func (g *pwRig) signIn(pw, addr string) *http.Cookie {
	g.t.Helper()
	w, _ := g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"`+pw+`"}`, addr, nil))
	require.Equal(g.t, http.StatusOK, w.Code, w.Body.String())
	return sessionCookie(g.t, w)
}

func foreign(r *http.Request) *http.Request {
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	return r
}

// Mutation: drop the sameOriginWrite check of ChangePassword, SignOutOthers or RevokeSessionByID: a page on another site changes the
// password or signs the person's browsers out.
func TestSessionAndPasswordWritesRefuseAForeignOrigin(t *testing.T) {
	g := newPwRig(t)
	g.setPassword("first password")
	a, b := g.signIn("first password", "10.0.0.7"), g.signIn("first password", "10.0.0.8")

	w, out := g.do(g.ar.ChangePassword, foreign(g.req("POST", "/api/auth/password/change", `{"current":"first password","password":"second password"}`, "10.0.0.7", a)))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "cross_origin", out["code"])
	phc, err := ReadPasswordHash(g.ks.dataDir())
	require.NoError(t, err)
	assert.True(t, VerifyPassword(phc, "first password"), "the password was not changed")
	assert.True(t, g.ks.ValidateSession(a.Value) && g.ks.ValidateSession(b.Value), "no session was ended")

	w, out = g.do(g.ar.SignOutOthers, foreign(g.req("POST", "/api/auth/sessions/sign-out-others", ``, "10.0.0.7", a)))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "cross_origin", out["code"])
	assert.True(t, g.ks.ValidateSession(b.Value), "the other browser stays signed in")

	id := sessionHash(b.Value)[:12]
	w, out = g.do(g.ar.RevokeSessionByID, foreign(g.req("DELETE", "/api/auth/sessions/"+id, ``, "10.0.0.7", a)))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "cross_origin", out["code"])
	assert.True(t, g.ks.ValidateSession(b.Value), "the listed browser stays signed in")
}

// Mutation: let a failed write fall through (no return after the save_failed answer): the sessions are ended and success is said although
// the old password is still the one in force; or answer 200.
func TestAPasswordWriteThatFailsAnswers500AndChangesNothing(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the process cannot write to")
	}
	g := newPwRig(t)
	g.setPassword("first password")
	a, b := g.signIn("first password", "10.0.0.7"), g.signIn("first password", "10.0.0.8")
	dir := g.ks.dataDir()
	require.NoError(t, os.Chmod(dir, 0o500)) // readable and searchable, not writable: the new file cannot be made
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	w, out := g.do(g.ar.ChangePassword, g.req("POST", "/api/auth/password/change", `{"current":"first password","password":"second password"}`, "10.0.0.7", a))
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.Equal(t, "save_failed", out["code"])
	assert.True(t, g.ks.ValidateSession(a.Value) && g.ks.ValidateSession(b.Value), "a failed change ends no session")

	g.cli = true
	w, out = g.do(g.ar.ResetPassword, g.req("POST", "/api/auth/password/reset", `{"password":"third password"}`, "127.0.0.1", nil))
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.Equal(t, "save_failed", out["code"])
	w, out = g.do(g.ar.ResetPassword, g.req("POST", "/api/auth/password/reset", `{"clear":true}`, "127.0.0.1", nil))
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.Equal(t, "save_failed", out["code"])
	assert.True(t, g.ks.ValidateSession(a.Value) && g.ks.ValidateSession(b.Value), "a failed reset ends no session")

	require.NoError(t, os.Chmod(dir, 0o700))
	phc, err := ReadPasswordHash(dir)
	require.NoError(t, err)
	assert.True(t, VerifyPassword(phc, "first password"), "the first password is still the one in force")
}

// Mutation: drop the length check of hashNew: a megabyte "password" is hashed (the cost is the attacker's to choose) or stored.
func TestANewPasswordOverTheLimitIsRefusedWithoutBeingStored(t *testing.T) {
	g := newPwRig(t)
	g.cli = true
	w, out := g.do(g.ar.SetPassword, g.req("POST", "/api/auth/password/set", `{"password":"`+strings.Repeat("a", MaxPasswordBytes+1)+`"}`, "127.0.0.1", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Equal(t, "too_long", out["code"])
	assert.False(t, g.ks.PasswordSet(), "nothing was stored")
	w, out = g.do(g.ar.SetPassword, g.req("POST", "/api/auth/password/set", `{"password":"`+strings.Repeat("a", MaxPasswordBytes)+`"}`, "127.0.0.1", nil))
	assert.Equal(t, http.StatusOK, w.Code, "a password at the limit is accepted: "+w.Body.String())
	assert.True(t, g.ks.PasswordSet())
}

// Mutation: drop the size check of readPrivateSecret: CORAL_INITIAL_PASSWORD_FILE pointed at a huge file reads and hashes all of it.
func TestInitialPasswordFileOverTheLimitSetsNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes")
	}
	g := newPwRig(t)
	path := filepath.Join(t.TempDir(), "initial-password")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", MaxPasswordBytes+10)+"\n"), 0o600))
	_, err := readPrivateSecret(path)
	assert.ErrorIs(t, err, ErrPasswordTooLong)
	g.ks.ApplyInitialPasswordFile(path)
	assert.False(t, g.ks.PasswordSet(), "no password from an over-long file")
	_, statErr := os.Stat(path)
	assert.NoError(t, statErr, "the file is left alone when it was not used")
}

// Mutation: GetSetupCode hands out a code while a password is set: the code would let anyone who sees it set a second one.
func TestNoSetupCodeOnceAPasswordIsSet(t *testing.T) {
	g := newPwRig(t)
	g.cli = true
	w, out := g.do(g.ar.GetSetupCode, g.req("GET", "/api/auth/setup-code", ``, "127.0.0.1", nil))
	require.Equal(t, http.StatusOK, w.Code, "before a password there is a code: "+w.Body.String())
	assert.NotEmpty(t, out["code"])
	g.setPassword("a password")
	w, out = g.do(g.ar.GetSetupCode, g.req("GET", "/api/auth/setup-code", ``, "127.0.0.1", nil))
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "password_set", out["code"])
	assert.Nil(t, out["expires_in"], "no code and no time left to give")
}

// Mutation: report a revoked or unknown cookie as authenticated, or the right one as not: the dashboard's status is what the page trusts.
func TestAuthStatusReportsOnlyALiveSessionCookie(t *testing.T) {
	g := newPwRig(t)
	g.ar.strictLocal = true
	g.setPassword("a password")
	c := g.signIn("a password", "10.0.0.7")
	status := func(cookie *http.Cookie) map[string]any {
		_, out := g.do(g.ar.AuthStatus, g.req("GET", "/api/system/auth-status", ``, "10.0.0.7", cookie))
		return out
	}
	out := status(c)
	assert.Equal(t, true, out["authenticated"])
	assert.Equal(t, "session", out["method"])
	assert.Equal(t, false, status(&http.Cookie{Name: c.Name, Value: "not-a-session"})["authenticated"])
	g.ks.RevokeSession(c.Value)
	out = status(c)
	assert.Equal(t, false, out["authenticated"], "a revoked cookie is not a credential")
	assert.Equal(t, "none", out["method"])
	assert.Equal(t, true, out["password_set"])
}
