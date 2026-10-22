package routes

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// The check driver (#5280 step 3 B): "Check now", "check automatically" and the background job. All local only, all 404 while the feature is off.

func (e *relaunchEnv) checkNow(hdr map[string]string) (int, map[string]any) {
	return e.do(e.h.CheckNow, "POST", "/api/system/update/check", "", hdr)
}

func (e *relaunchEnv) setAuto(body string, hdr map[string]string) (int, map[string]any) {
	return e.do(e.h.SetAuto, "PUT", "/api/system/update/settings", body, hdr)
}

func TestTheCheckRoutesAre404WhileTheFeatureIsOffAndTheJobDoesNothing(t *testing.T) {
	e := newRelaunchEnv(t)
	e.on = false
	code, _ := e.checkNow(nil)
	assert.Equal(t, 404, code)
	code, _ = e.setAuto(`{"auto": false}`, map[string]string{"Content-Type": "application/json"})
	assert.Equal(t, 404, code)

	ran := make(chan struct{})
	e.h.checkTimes = checkTimes{after: func(time.Duration) <-chan time.Time { close(ran); return nil }}
	done := make(chan struct{})
	go func() { e.h.RunBackgroundChecks(t.Context()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the job waits while the feature is off")
	}
	select {
	case <-ran:
		t.Fatal("the job scheduled a check while the feature is off")
	default:
	}
	assert.Zero(t, e.up.checkCount())
}

func TestCheckNowIsLocalOnlyAndStartsOneCheck(t *testing.T) {
	e := newRelaunchEnv(t)
	code, out := e.checkNow(hubHdr(ownerA))
	assert.Equal(t, 403, code, "never through Coral Hub, whoever it says it is")
	assert.Equal(t, "local_only", out["code"])
	assert.Zero(t, e.up.checkCount())

	code, out = e.checkNow(nil)
	require.Equal(t, 202, code, "%v", out)
	require.Eventually(t, func() bool { return e.up.checkCount() == 1 }, 5*time.Second, 5*time.Millisecond)
}

func TestCheckNowRefusesWhatTheUpdaterWouldNotUpdate(t *testing.T) {
	e := newRelaunchEnv(t)
	e.up.rec.Supported = false
	e.up.rec.InstallKind = installHomebrew
	code, out := e.checkNow(nil)
	assert.Equal(t, 409, code)
	assert.Equal(t, reasonUnsupported, out["code"])
	assert.Equal(t, "homebrew", out["install_kind"])
	assert.Equal(t, upgradeCommandHomebrew, out["upgrade_command"], "a Homebrew install is pointed to brew")

	e.up.rec.Supported, e.up.rec.InstallKind, e.up.rec.Enabled = true, "", false
	code, out = e.checkNow(nil)
	assert.Equal(t, 409, code)
	assert.Equal(t, reasonSwitchOff, out["code"])
	assert.Zero(t, e.up.checkCount(), "nothing was started")
}

func TestCheckNowIsOneAtATimeAndOneAMinute(t *testing.T) {
	e := newRelaunchEnv(t)
	e.up.checkBlock = make(chan struct{})
	code, _ := e.checkNow(nil)
	require.Equal(t, 202, code)
	require.Eventually(t, func() bool { return e.up.checkCount() == 1 }, 5*time.Second, 5*time.Millisecond)

	e.now = e.now.Add(2 * manualCheckSpacing) // a minute passed, but the check is still running
	code, out := e.checkNow(nil)
	assert.Equal(t, 409, code)
	assert.Equal(t, reasonBusy, out["code"])
	close(e.up.checkBlock)
	require.Eventually(t, func() bool { return !e.h.checking.Load() }, 5*time.Second, 5*time.Millisecond)

	// the one-a-minute limit counts from the start of the last check a person made
	code, _ = e.checkNow(nil)
	require.Equal(t, 202, code, "a minute after the first one")
	e.now = e.now.Add(manualCheckSpacing / 2)
	require.Eventually(t, func() bool { return !e.h.checking.Load() }, 5*time.Second, 5*time.Millisecond)
	req := httptest.NewRequest("POST", "/api/system/update/check", nil)
	rec := httptest.NewRecorder()
	e.h.CheckNow(rec, req)
	assert.Equal(t, 429, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), "too_soon")
	assert.Equal(t, 2, e.up.checkCount())
}

func TestSetAutoSavesOnlyTheOneFieldFromTheLocalDashboard(t *testing.T) {
	e := newRelaunchEnv(t)
	var mu sync.Mutex
	saved := map[string]string{}
	e.h.SetSetting = func(_ context.Context, k, v string) error { mu.Lock(); defer mu.Unlock(); saved[k] = v; return nil }
	js := map[string]string{"Content-Type": "application/json"}

	code, out := e.setAuto(`{"auto": false}`, js)
	require.Equal(t, 200, code, "%v", out)
	assert.Equal(t, false, out["auto"])
	assert.Equal(t, map[string]string{UpdateAutoSetting: "false"}, saved)
	code, _ = e.setAuto(`{"auto": true}`, js)
	require.Equal(t, 200, code)
	assert.Equal(t, "true", saved[UpdateAutoSetting])

	hub := map[string]string{"Content-Type": "application/json"}
	for k, v := range hubHdr(ownerA) {
		hub[k] = v
	}
	code, out = e.setAuto(`{"auto": false}`, hub)
	assert.Equal(t, 403, code, "never through Coral Hub")
	assert.Equal(t, "local_only", out["code"])
	assert.Equal(t, "true", saved[UpdateAutoSetting], "and nothing was saved")

	for name, body := range map[string]string{
		"empty": ``, "no field": `{}`, "a string": `{"auto": "false"}`, "null": `{"auto": null}`, "another field": `{"auto": true, "channel": "agentic"}`,
		"two objects": `{"auto": true}{"auto": false}`, "an array": `[true]`, "not json": `auto=false`,
	} {
		code, _ = e.setAuto(body, js)
		assert.Equal(t, 400, code, name)
	}
	assert.Equal(t, "true", saved[UpdateAutoSetting])
	code, _ = e.setAuto(`{"auto": false}`, nil)
	assert.GreaterOrEqual(t, code, 400, "a body that does not say it is JSON is refused")
	assert.Equal(t, "true", saved[UpdateAutoSetting])

	big := `{"auto": true, "x": "` + strings.Repeat("a", autoBodyMax) + `"}`
	code, _ = e.setAuto(big, js)
	assert.Equal(t, 400, code)
}

func TestTheScheduleIsTheIntervalDoubledPerFailureAndJitteredAndCapped(t *testing.T) {
	tm := checkTimes{jitterOf: func(max time.Duration) time.Duration { return max }}.get()
	assert.Equal(t, 6*time.Hour+30*time.Minute, tm.nextCheckWait(0))
	assert.Equal(t, 12*time.Hour+30*time.Minute, tm.nextCheckWait(1))
	assert.Equal(t, 24*time.Hour+30*time.Minute, tm.nextCheckWait(2))
	assert.Equal(t, 24*time.Hour+30*time.Minute, tm.nextCheckWait(9), "a day at most, then the jitter")
	zero := checkTimes{jitterOf: func(time.Duration) time.Duration { return 0 }}.get()
	assert.Equal(t, 6*time.Hour, zero.nextCheckWait(0))
	assert.Equal(t, 5*time.Minute, zero.first, "the first check is a few minutes after Coral starts")
	for i := 0; i < 50; i++ {
		d := checkTimes{}.get().nextCheckWait(0)
		assert.GreaterOrEqual(t, d, 6*time.Hour)
		assert.LessOrEqual(t, d, 6*time.Hour+30*time.Minute)
	}
}

func TestTheJobChecksOnlyWhenAutomaticChecksAreOnTheUpdaterIsOnAndTheInstallIsOneItUpdates(t *testing.T) {
	for name, tc := range map[string]struct {
		mod  func(e *relaunchEnv)
		runs int
	}{
		"on by default":               {func(e *relaunchEnv) {}, 1},
		"automatic checks turned off": {func(e *relaunchEnv) { e.settings[UpdateAutoSetting] = "false" }, 0},
		"the updater is off":          {func(e *relaunchEnv) { e.up.rec.Enabled = false }, 0},
		"a Homebrew install":          {func(e *relaunchEnv) { e.up.rec.Supported, e.up.rec.InstallKind = false, installHomebrew }, 0},
		"a check is already running":  {func(e *relaunchEnv) { e.h.checking.Store(true) }, 0},
	} {
		e := newRelaunchEnv(t)
		tc.mod(e)
		ok := e.h.backgroundCheck(t.Context())
		assert.True(t, ok, name)
		assert.Equal(t, tc.runs, e.up.checkCount(), name)
	}

	e := newRelaunchEnv(t)
	e.up.checkResult = "failed"
	assert.False(t, e.h.backgroundCheck(t.Context()), "a check that failed counts as a failure")
	e = newRelaunchEnv(t)
	e.up.checkErr = errors.New("coral-update did not run")
	assert.False(t, e.h.backgroundCheck(t.Context()))
	e = newRelaunchEnv(t)
	e.up.checkResult = "backoff"
	assert.True(t, e.h.backgroundCheck(t.Context()), "the updater's own backoff is not a failure of the job")
	assert.False(t, e.h.checking.Load(), "the flag is released")
}

func TestTheJobWaitsFirstThenChecksAndDoublesTheWaitAfterAFailure(t *testing.T) {
	e := newRelaunchEnv(t)
	e.up.checkResult = "failed"
	waits := make(chan time.Duration, 8)
	e.h.checkTimes = checkTimes{jitterOf: func(time.Duration) time.Duration { return 0 },
		after: func(d time.Duration) <-chan time.Time {
			waits <- d
			c := make(chan time.Time, 1)
			c <- time.Now()
			return c
		}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { e.h.RunBackgroundChecks(ctx); close(done) }()
	var got []time.Duration
	for i := 0; i < 4; i++ {
		select {
		case d := <-waits:
			got = append(got, d)
		case <-time.After(5 * time.Second):
			t.Fatal("the job stopped scheduling")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the job did not end with its context")
	}
	assert.Equal(t, []time.Duration{5 * time.Minute, 12 * time.Hour, 24 * time.Hour, 24 * time.Hour}, got, "first a few minutes, then each failure doubles the wait up to a day")
}

// "Check automatically" is changed only through its own route: the generic settings write refuses it.
func TestTheGenericSettingsWriteRefusesTheAutoSwitch(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	h := NewSystemHandler(db, nil)
	r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"`+UpdateAutoSetting+`":"false"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.PutSettings(rec, r)
	assert.Equal(t, 400, rec.Code, rec.Body.String())
	s, _ := store.NewSessionStore(db).GetSettings(t.Context())
	assert.Empty(t, s[UpdateAutoSetting])
}
