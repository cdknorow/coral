package routes

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The local relaunch (#5280 step 3): the person at this computer relaunches into the staged update with the same acknowledgement token the Hub's owner uses, from a route of its own that
// is never carried through the Hub. The Hub's mode cap, Operator opt-out and switch are about what Coral Hub may do and do not apply to the person here.

func (e *relaunchEnv) postLocal(body string, hdr map[string]string) (int, map[string]any) {
	return e.do(e.h.RelaunchLocal, "POST", "/api/system/update/relaunch-local", body, hdr)
}

func (e *relaunchEnv) localToken() string {
	e.t.Helper()
	code, st := e.status(nil)
	require.Equal(e.t, 200, code)
	rl := st["relaunch"].(map[string]any)
	require.Equal(e.t, true, rl["available"], "%v", rl)
	return rl["ack"].(string)
}

func TestTheLocalStatusIssuesTheAcknowledgementWhateverTheHubMayDo(t *testing.T) {
	e := newRelaunchEnv(t)
	e.settings[MaxModeSetting] = "view"
	e.settings[HubRelaunchSetting] = "false"
	e.settings["hub_operator_disabled"] = "true"
	code, st := e.status(hubHdr(ownerA))
	require.Equal(t, 200, code)
	hub := st["relaunch"].(map[string]any)
	assert.Equal(t, false, hub["available"], "the Hub's owner is refused by its mode cap and its switch")
	assert.NotContains(t, hub, "ack")

	tok := e.localToken()
	assert.NotEmpty(t, tok, "the person at this computer is not")
	code, _ = e.postLocal(ack(tok), nil)
	assert.Equal(t, 202, code)
}

func TestTheLocalRelaunchStartsTheUpdaterOnceAndAnswersARepeatTheSame(t *testing.T) {
	e := newRelaunchEnv(t)
	tok := e.localToken()
	code, out := e.postLocal(ack(tok), nil)
	require.Equal(t, 202, code, "%v", out)
	assert.Equal(t, "op-1", out["operation"])
	require.Equal(t, []string{"local"}, e.up.applies, "started by the person here, not a Hub user")

	code, again := e.postLocal(ack(tok), nil) // a double click, a retry
	assert.Equal(t, 202, code)
	assert.Equal(t, out["operation"], again["operation"])
	assert.Len(t, e.up.applies, 1, "a token used once starts nothing the second time")
	e.up.mu.Lock()
	n := len(e.up.audits)
	last := e.up.audits[n-1].Code
	by := e.up.audits[n-1].By
	e.up.mu.Unlock()
	assert.Equal(t, "repeat", last)
	assert.Equal(t, "local", by)
}

func TestALocalTokenIsNoGoodOnTheHubsRouteAndAHubTokenIsNoGoodHere(t *testing.T) {
	e := newRelaunchEnv(t)
	local := e.localToken()
	code, out := e.post(ack(local), hubHdr(ownerA))
	assert.Equal(t, 409, code, "%v", out)
	assert.Equal(t, "stale_ack", out["code"])

	hub := e.token(ownerA)
	code, out = e.postLocal(ack(hub), nil)
	assert.Equal(t, 409, code, "%v", out)
	assert.Equal(t, "stale_ack", out["code"])
	assert.Empty(t, e.up.applies)
}

func TestTheLocalRelaunchIsLocalOnlyAndOffWhileTheFeatureIsOff(t *testing.T) {
	e := newRelaunchEnv(t)
	tok := e.localToken()
	code, out := e.postLocal(ack(tok), hubHdr(ownerA))
	assert.Equal(t, 403, code, "never through Coral Hub, the owner included")
	assert.Equal(t, "local_only", out["code"])
	assert.Empty(t, e.up.applies)

	e.on = false
	code, _ = e.postLocal(ack(tok), nil)
	assert.Equal(t, 404, code)
	assert.Empty(t, e.up.applies)
}

func TestTheLocalRelaunchKeepsEveryRuleOfTheRelaunchThatIsNotAboutTheHub(t *testing.T) {
	e := newRelaunchEnv(t)
	for name, body := range map[string]string{"no body": ``, "no token": `{}`, "another field": `{"ack":"x","version":"9.9.9"}`, "a bad token": `{"ack":"not-a-token"}`} {
		code, _ := e.postLocal(body, nil)
		assert.GreaterOrEqual(t, code, 400, name)
	}
	assert.Empty(t, e.up.applies)

	// nothing staged, not verified, an install the updater does not update, an update being applied: no token, and a POST says why
	for name, mod := range map[string]func(r *UpdaterRecord){
		"nothing staged":   func(r *UpdaterRecord) { r.Staged = nil },
		"not verified":     func(r *UpdaterRecord) { r.Staged.Verified = false },
		"unsupported":      func(r *UpdaterRecord) { r.Supported = false },
		"already applying": func(r *UpdaterRecord) { r.State = updApplying },
	} {
		e = newRelaunchEnv(t)
		tok := e.localToken()
		mod(&e.up.rec)
		code, st := e.status(nil)
		require.Equal(t, 200, code, name)
		rl := st["relaunch"].(map[string]any)
		assert.Equal(t, false, rl["available"], name)
		assert.NotContains(t, rl, "ack", name)
		code, _ = e.postLocal(ack(tok), nil)
		assert.GreaterOrEqual(t, code, 409, name+": the token that was good a moment ago starts nothing")
		assert.Empty(t, e.up.applies, name)
	}

	// the spacing: a relaunch started a moment ago
	e = newRelaunchEnv(t)
	tok := e.localToken()
	e.up.rec.LastHubRelaunch = e.now.Add(-time.Minute)
	code, _ := e.postLocal(ack(tok), nil)
	assert.Equal(t, http.StatusTooManyRequests, code)
	assert.Empty(t, e.up.applies)
}

// A relaunch that ended without an update (the apply never got ready, or ended first) can be tried again after the spacing; it is not "already started" for ever.
func TestARelaunchThatEndedWithoutAnUpdateCanBeTriedAgainAfterTheSpacing(t *testing.T) {
	e := newRelaunchEnv(t)
	code, _ := e.postLocal(ack(e.localToken()), nil)
	require.Equal(t, 202, code)

	// the updater's record: nothing is running, a result since the start, the version no longer marked as relaunched
	e.now = e.now.Add(relaunchSpacing + time.Minute)
	e.up.mu.Lock()
	e.up.rec.State = updStaged
	e.up.rec.HubRelaunched = ""
	e.up.rec.LastHubRelaunch = e.now.Add(-relaunchSpacing - 30*time.Second)
	e.up.rec.LastResult = &UpdateResult{At: e.now.Add(-30 * time.Second), From: "1.4.130", To: "1.4.131", Result: resultRefused, By: "local"}
	e.up.mu.Unlock()
	code, _ = e.postLocal(ack(e.localToken()), nil)
	assert.Equal(t, 202, code, "tried again")
	assert.Len(t, e.up.applies, 2)
}
