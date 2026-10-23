package routes

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// The local relaunch (#5280 step 3): the person at this computer relaunches into the staged update with the same acknowledgement token the Hub's owner uses, from a route of its own that
// is never carried through the Hub. The Hub's mode cap, Operator opt-out and switch are about what Coral Hub may do and do not apply to the person here.

func (e *relaunchEnv) postLocal(body string, hdr map[string]string) (int, map[string]any) {
	return e.do(asLocalPerson(e.h.RelaunchLocal), "POST", "/api/system/update/relaunch-local", body, hdr)
}

// asLocalPerson stands for the dashboard on this computer: the request comes from the person's browser, the one caller kind a yes may come from.
func asLocalPerson(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h(w, r.WithContext(context.WithValue(r.Context(), callerKindKey{}, callerDashCookie)))
	}
}

// #6290: the install is a person's yes. An agent's session token, a request with no caller kind and one the Hub carries are each refused with nothing started, and the
// token stays good for the person's own click afterwards.
func TestTheLocalRelaunchIsNeverAnAgentsOrAnUnknownCallersYes(t *testing.T) {
	e := newRelaunchEnv(t)
	tok := e.localToken()
	forged := map[string]string{"Origin": "http://127.0.0.1:8420", "Sec-Fetch-Site": "same-origin"} // what a script copies from a browser
	for name, c := range map[string]struct {
		h   http.HandlerFunc
		hdr map[string]string
	}{
		"an agent's session token":              {asLocalPerson(e.h.RelaunchLocal), map[string]string{store.SessionTokenHeader: "agent-token"}},
		"an agent's token with browser headers": {asLocalPerson(e.h.RelaunchLocal), map[string]string{store.SessionTokenHeader: "agent-token", "Origin": forged["Origin"], "Sec-Fetch-Site": "same-origin"}},
		"no caller kind, browser headers":       {e.h.RelaunchLocal, forged},
	} {
		code, out := e.do(c.h, "POST", "/api/system/update/relaunch-local", ack(tok), c.hdr)
		assert.Equal(t, 403, code, "%s: %v", name, out)
		assert.Empty(t, e.up.applies, name+": nothing started")
	}
	code, _ := e.postLocal(ack(tok), nil)
	assert.Equal(t, 202, code, "the person's own click still works with the same token")
	assert.Equal(t, []string{"local"}, e.up.applies)
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

// #6290: hub_allowed says what the settings say about the Hub's owner, to the person here too: off until the switch was turned on, and off again under the cap or the Operator opt-out.
func TestHubAllowedFollowsTheSettingsWhoeverAsks(t *testing.T) {
	allowed := func(e *relaunchEnv, hdr map[string]string) any {
		code, st := e.status(hdr)
		require.Equal(t, 200, code)
		return st["relaunch"].(map[string]any)["hub_allowed"]
	}
	e := newRelaunchEnv(t)
	for name, c := range map[string]struct {
		set  map[string]string
		want bool
	}{
		"turned on":                {map[string]string{HubRelaunchSetting: "true"}, true},
		"never touched":            {map[string]string{HubRelaunchSetting: ""}, false},
		"turned off":               {map[string]string{HubRelaunchSetting: "false"}, false},
		"on, under a view cap":     {map[string]string{HubRelaunchSetting: "true", MaxModeSetting: "view"}, false},
		"on, the Operator opt-out": {map[string]string{HubRelaunchSetting: "true", "hub_operator_disabled": "true"}, false},
	} {
		e.settings[MaxModeSetting] = "launch"
		delete(e.settings, "hub_operator_disabled")
		for k, v := range c.set {
			e.settings[k] = v
		}
		assert.Equal(t, c.want, allowed(e, nil), name+": the person here sees it")
		assert.Equal(t, c.want, allowed(e, hubHdr(ownerA)), name+": the Hub's owner sees it")
	}
}
