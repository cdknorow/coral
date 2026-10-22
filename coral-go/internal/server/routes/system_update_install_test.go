package routes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// The local "Install update" (#166): a person at this computer only, no body, Windows only, and the server quits only once the updater is running.
func TestInstallUpdateIsLocalPersonOnly(t *testing.T) {
	old := updateInstallPlatform
	updateInstallPlatform = true
	t.Cleanup(func() { updateInstallPlatform = old })

	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	h := NewCloudHandler(store.NewSessionStore(db), t.TempDir())
	peer := [2]bool{true, true}
	h.SetPeerCheck(func(*http.Request) (bool, bool) { return peer[0], peer[1] })

	var launched, quit int
	var launchErr error
	h.stagedUpdate = func(context.Context) (string, error) { return "1.5.0", nil }
	h.launchUpdater = func(context.Context) error { launched++; return launchErr }
	h.QuitForUpdate = func() error { quit++; return nil }

	req := func(body string, mut func(*http.Request) *http.Request) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8420/api/system/update/install", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set("Origin", "http://127.0.0.1:8420")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r = r.WithContext(context.WithValue(r.Context(), callerKindKey{}, callerDashCookie))
		if mut != nil {
			r = mut(r)
		}
		rec := httptest.NewRecorder()
		h.InstallUpdate(rec, r)
		return rec
	}
	reset := func() { launched, quit, launchErr = 0, 0, nil; h.installing = 0 }

	t.Run("the dashboard on this computer starts the updater, then quits", func(t *testing.T) {
		reset()
		rec := req("", nil)
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		assert.JSONEq(t, `{"ok":true,"version":"1.5.0","quitting":true}`, rec.Body.String())
		assert.Equal(t, [2]int{1, 1}, [2]int{launched, quit})
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	})
	t.Run("an empty object is the same request", func(t *testing.T) {
		reset()
		require.Equal(t, http.StatusAccepted, req("{}", nil).Code)
	})
	t.Run("the request names nothing: a body that chooses a version or a path is refused", func(t *testing.T) {
		for _, body := range []string{`{"version":"9.9.9"}`, `{"path":"C:\\x.msi"}`, `{"url":"http://x"}`, strings.Repeat(" ", 40)} {
			reset()
			rec := req(body, nil)
			assert.Equal(t, http.StatusBadRequest, rec.Code, body)
			assert.Equal(t, [2]int{0, 0}, [2]int{launched, quit}, body)
		}
	})
	t.Run("through Coral Hub it is refused", func(t *testing.T) {
		reset()
		rec := req("", func(r *http.Request) *http.Request { return stamp(r) })
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, [2]int{0, 0}, [2]int{launched, quit})
	})
	t.Run("an agent's session token is refused, whatever else the request carries", func(t *testing.T) {
		reset()
		rec := req("", func(r *http.Request) *http.Request {
			r.Header.Set(store.SessionTokenHeader, "an-agents-token")
			return r
		})
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Equal(t, [2]int{0, 0}, [2]int{launched, quit})
	})
	t.Run("another local user is refused", func(t *testing.T) {
		reset()
		peer = [2]bool{false, true}
		t.Cleanup(func() { peer = [2]bool{true, true} })
		assert.Equal(t, http.StatusForbidden, req("", nil).Code)
		assert.Equal(t, [2]int{0, 0}, [2]int{launched, quit})
	})
	t.Run("a request that is not the dashboard (no browser marks) is refused", func(t *testing.T) {
		reset()
		rec := req("", func(r *http.Request) *http.Request {
			r.Header.Del("Origin")
			r.Header.Del("Sec-Fetch-Site")
			return r.WithContext(context.Background())
		})
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, [2]int{0, 0}, [2]int{launched, quit})
	})
	t.Run("not Windows: 409, nothing started", func(t *testing.T) {
		reset()
		updateInstallPlatform = false
		t.Cleanup(func() { updateInstallPlatform = true })
		rec := req("", nil)
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, rec.Body.String(), "unsupported_platform")
		assert.Equal(t, [2]int{0, 0}, [2]int{launched, quit})
	})
	t.Run("nothing staged: 409, the server keeps running", func(t *testing.T) {
		reset()
		h.stagedUpdate = func(context.Context) (string, error) { return "", errors.New("none") }
		t.Cleanup(func() { h.stagedUpdate = func(context.Context) (string, error) { return "1.5.0", nil } })
		rec := req("", nil)
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, rec.Body.String(), "nothing_staged")
		assert.Equal(t, [2]int{0, 0}, [2]int{launched, quit})
	})
	t.Run("no way to quit for the updater (not under the tray): 409, the updater is not started", func(t *testing.T) {
		reset()
		keep := h.QuitForUpdate
		h.QuitForUpdate = nil
		t.Cleanup(func() { h.QuitForUpdate = keep })
		rec := req("", nil)
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, rec.Body.String(), "install_unavailable")
		assert.Zero(t, launched)
	})
	t.Run("the updater could not be started: 500, and the server does NOT quit", func(t *testing.T) {
		reset()
		launchErr = errors.New("boom")
		rec := req("", nil)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Contains(t, rec.Body.String(), "updater_not_started")
		assert.Equal(t, [2]int{1, 0}, [2]int{launched, quit})
		// and it can be tried again
		launchErr = nil
		assert.Equal(t, http.StatusAccepted, req("", nil).Code)
	})
	t.Run("a second request while one is under way is refused", func(t *testing.T) {
		reset()
		require.Equal(t, http.StatusAccepted, req("", nil).Code)
		rec := req("", nil)
		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.Contains(t, rec.Body.String(), "busy")
		assert.Equal(t, [2]int{1, 1}, [2]int{launched, quit})
	})
}

// On Windows the Hub's relaunch is refused (#166): the install needs a person at this computer to approve it, so Coral Hub can neither start it nor
// leave Coral stopped behind a prompt nobody sees. Elsewhere it is as before (TestRelaunchHappyPathAndStatusShape).
func TestRelaunchIsRefusedOnWindows(t *testing.T) {
	e := newRelaunchEnv(t) // (it stands for a non-Windows OS; the next line stands for Windows; its cleanup restores the real value)
	relaunchNeedsLocalApproval = true

	code, st := e.status(hubHdr(ownerA))
	require.Equal(t, 200, code)
	rl := st["relaunch"].(map[string]any)
	assert.Equal(t, false, rl["available"])
	assert.Equal(t, "local_approval_needed", rl["reason"])
	assert.NotContains(t, rl, "ack", "no token is issued for a relaunch that cannot start")

	for _, body := range []string{`{"ack":"anything"}`, "", "{}"} {
		code, out := e.post(body, hubHdr(ownerA))
		assert.Equal(t, 409, code, body)
		assert.Equal(t, "local_approval_needed", out["code"], "%v", out)
	}
	assert.Zero(t, e.up.applyCount(), "nothing was started")
}
