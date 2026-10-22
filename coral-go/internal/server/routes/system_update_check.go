package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/safego"
)

// The check driver (#5280 step 3 B): POST /api/system/update/check (a person's "Check now"), PUT /api/system/update/settings {"auto": bool} (check automatically) and the background job
// that runs "coral-update check" on a schedule. A check finds the newest promoted release, verifies it and stages it; it never installs. Installing always needs a person's relaunch with its
// acknowledgement. Both routes are local only (never through Coral Hub, and an agent's token is refused), answer 404 while the feature is off, and the job does nothing then either.

const (
	// manualCheckSpacing is the least time between two checks a person starts.
	manualCheckSpacing = time.Minute
	// checkRunLimit is how long one check may run (coral-update's own cap).
	checkRunLimit = 30 * time.Minute
	// the job's schedule: the first check a few minutes after Coral starts, then every six hours with up to half an hour of jitter; failures double the wait up to a day.
	firstCheckAfter = 5 * time.Minute
	checkEvery      = 6 * time.Hour
	checkJitter     = 30 * time.Minute
	checkBackoffMax = 24 * time.Hour
	autoBodyMax     = 1 << 10
)

// ErrCheckBusy: a check is already running.
var ErrCheckBusy = errors.New("a check is already running")

// checkTimes is the background job's timing and clock (a test replaces them).
type checkTimes struct {
	first, every, jitter, backoffMax time.Duration
	after                            func(time.Duration) <-chan time.Time
	jitterOf                         func(max time.Duration) time.Duration
}

func (t checkTimes) get() checkTimes {
	if t.first == 0 {
		t.first = firstCheckAfter
	}
	if t.every == 0 {
		t.every = checkEvery
	}
	if t.jitter == 0 {
		t.jitter = checkJitter
	}
	if t.backoffMax == 0 {
		t.backoffMax = checkBackoffMax
	}
	if t.after == nil {
		t.after = time.After
	}
	if t.jitterOf == nil {
		t.jitterOf = func(max time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(max) + 1)) }
	}
	return t
}

// nextCheckWait is how long until the next background check: the interval, doubled for each failure in a row (at most a day), and the jitter.
func (t checkTimes) nextCheckWait(failures int) time.Duration {
	w := t.every
	for i := 0; i < failures && w < t.backoffMax; i++ {
		w *= 2
	}
	return min(w, t.backoffMax) + t.jitterOf(t.jitter)
}

// notCheckable says why a check would be pointless or refused now: the updater is off, or this install is one the updater does not update (Homebrew's cask, a package manager's folder).
func notCheckable(rec UpdaterRecord) string {
	switch {
	case !rec.Enabled:
		return reasonSwitchOff
	case !rec.Supported:
		return reasonUnsupported
	}
	return ""
}

func (h *UpdateRelaunchHandler) localOnly(w http.ResponseWriter, r *http.Request) bool {
	if viaTunnel(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "local_only", "error": "this is for this computer's own dashboard and command line"})
		return false
	}
	return true
}

// CheckNow: POST /api/system/update/check. Starts one check in the background (202), at most one at a time and one a minute.
func (h *UpdateRelaunchHandler) CheckNow(w http.ResponseWriter, r *http.Request) {
	if !h.on() {
		errNotFound(w, "not found")
		return
	}
	if !h.localOnly(w, r) {
		return
	}
	rec, err := h.Updater.Record(r.Context())
	if err != nil {
		slog.Warn("[update] status unavailable for a check", "error", err)
		errInternalServer(w, "the update check is unavailable")
		return
	}
	if why := notCheckable(rec); why != "" {
		body := map[string]any{"code": why, "error": "this computer does not check for updates itself"}
		if rec.InstallKind != "" && installKinds[rec.InstallKind] {
			body["install_kind"] = rec.InstallKind
		}
		if rec.InstallKind == installHomebrew {
			body["upgrade_command"] = upgradeCommandHomebrew
		}
		writeJSON(w, http.StatusConflict, body)
		return
	}
	h.checkMu.Lock()
	now := h.now()
	if wait := manualCheckSpacing - now.Sub(h.lastManual); !h.lastManual.IsZero() && wait > 0 {
		h.checkMu.Unlock()
		httputil.WriteActionLimit(w, int(wait/time.Second)+1, reasonTooSoon, "a check was started a moment ago")
		return
	}
	if !h.checking.CompareAndSwap(false, true) {
		h.checkMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"code": reasonBusy, "error": "a check is already running"})
		return
	}
	h.lastManual = now
	h.checkMu.Unlock()
	safego.Go("update check", func() {
		defer h.checking.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), checkRunLimit)
		defer cancel()
		if res, err := h.Updater.Check(ctx); err != nil {
			slog.Warn("[update] the check could not run", "error", err)
		} else {
			slog.Info("[update] check finished", "result", res)
		}
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// SetAuto: PUT /api/system/update/settings {"auto": bool}: whether Coral checks for updates (and keeps one staged) by itself. The one object, one field, at most 1 KB.
func (h *UpdateRelaunchHandler) SetAuto(w http.ResponseWriter, r *http.Request) {
	if !h.on() {
		errNotFound(w, "not found")
		return
	}
	if !h.localOnly(w, r) || !requireJSON(w, r) {
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, autoBodyMax))
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var body struct {
		Auto *bool `json:"auto"`
	}
	if err != nil || dec.Decode(&body) != nil || body.Auto == nil {
		errBadRequest(w, `the body is exactly {"auto": true or false}`)
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		errBadRequest(w, `the body is exactly {"auto": true or false}`)
		return
	}
	if h.SetSetting == nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	if err := h.SetSetting(r.Context(), UpdateAutoSetting, strconv.FormatBool(*body.Auto)); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"auto": *body.Auto})
}

// RunBackgroundChecks is the job: it returns at once while the feature is off (nothing runs), else waits for ctx and checks on the schedule, only when the owner has not turned automatic
// checks off, the updater is on and this install is one it updates. A failure doubles the wait (up to a day); a check that is already running (a person's) is not doubled.
func (h *UpdateRelaunchHandler) RunBackgroundChecks(ctx context.Context) {
	if !h.on() {
		return
	}
	t := h.checkTimes.get()
	wait, failures := t.first, 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.after(wait):
		}
		if h.backgroundCheck(ctx) {
			failures = 0
		} else {
			failures++
		}
		wait = t.nextCheckWait(failures)
	}
}

// backgroundCheck runs one scheduled check when it should; false only when a check ran and failed.
func (h *UpdateRelaunchHandler) backgroundCheck(ctx context.Context) bool {
	settings, err := h.Settings(ctx)
	if err != nil || !UpdateAutoOn(settings) {
		return true
	}
	rec, err := h.Updater.Record(ctx)
	if err != nil {
		return false
	}
	if notCheckable(rec) != "" {
		return true
	}
	if !h.checking.CompareAndSwap(false, true) {
		return true // a check is running already
	}
	defer h.checking.Store(false)
	cctx, cancel := context.WithTimeout(ctx, checkRunLimit)
	defer cancel()
	res, err := h.Updater.Check(cctx)
	if err != nil {
		slog.Warn("[update] the scheduled check could not run", "error", err)
		return false
	}
	slog.Info("[update] scheduled check finished", "result", res)
	return res != "failed"
}
