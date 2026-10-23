package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tray's setup code item (#6410): it reads the server's answer to the command line's route, says the code and how long it lasts, goes when a password is set,
// and refuses an answer that is not a code.
func TestAskServerForSetupCode(t *testing.T) {
	serve := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/auth/setup-code", r.URL.Path)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}
	dir := t.TempDir()

	ok := serve(200, `{"code":"H2XR-HS16","expires_in":3540}`)
	defer ok.Close()
	a, err := askServerForSetupCode(ok.URL, dir)
	require.NoError(t, err)
	assert.Equal(t, "H2XR-HS16", a.Code)
	assert.Equal(t, "Setup code: H2XR-HS16 (59 minutes left)", setupCodeTitle(a))

	set := serve(409, `{"code":"password_set"}`)
	defer set.Close()
	_, err = askServerForSetupCode(set.URL, dir)
	assert.ErrorIs(t, err, errNoSetupCode)

	refused := serve(403, `{"code":"cli_only"}`)
	defer refused.Close()
	_, err = askServerForSetupCode(refused.URL, dir)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errNoSetupCode)

	for name, body := range map[string]string{"not json": `x`, "no code": `{}`, "wrong shape": `{"code":"NOTACODE12","expires_in":5}`} {
		bad := serve(200, body)
		_, err := askServerForSetupCode(bad.URL, dir)
		bad.Close()
		assert.Error(t, err, name)
	}

	assert.Equal(t, "Setup code: ABCD-EFGH (1 minute left)", setupCodeTitle(setupCodeAnswer{Code: "ABCD-EFGH", ExpiresIn: 20}))
	assert.Equal(t, "Setup code: ABCD-EFGH (1 minute left)", setupCodeTitle(setupCodeAnswer{Code: "ABCD-EFGH", ExpiresIn: 0}))
}

type fakeMenuItem struct {
	mu     sync.Mutex
	title  string
	hidden bool
}

func (f *fakeMenuItem) SetTitle(s string) { f.mu.Lock(); f.title = s; f.mu.Unlock() }
func (f *fakeMenuItem) Show()             { f.mu.Lock(); f.hidden = false; f.mu.Unlock() }
func (f *fakeMenuItem) Hide()             { f.mu.Lock(); f.hidden = true; f.mu.Unlock() }
func (f *fakeMenuItem) get() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.title, f.hidden
}

// The code is on the menu line only for a short while after the click (Security's #6410 LOW): then the item is "Show setup code" again, and a second click's own timer is the one that counts.
func TestSetupCodeItemFallsBackAfterTheClick(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"H2XR-HS16","expires_in":3540}`))
	}))
	defer srv.Close()
	f := &fakeMenuItem{}
	s := &setupCodeItem{item: f, hideIn: 150 * time.Millisecond}
	assert.Equal(t, 2*time.Minute, newSetupCodeItem(nil).hideIn, "two minutes in the menu")

	s.click(srv.URL, t.TempDir())
	title, hidden := f.get()
	assert.Equal(t, "Setup code: H2XR-HS16 (59 minutes left)", title)
	assert.False(t, hidden)
	assert.Eventually(t, func() bool { ti, _ := f.get(); return ti == setupCodeMenuTitle }, 2*time.Second, 10*time.Millisecond, "the plain item is back")

	// a second click inside the window restarts it: the first timer must not take the code off early
	s.hideIn = 300 * time.Millisecond
	s.click(srv.URL, t.TempDir())
	time.Sleep(200 * time.Millisecond) // sleep-ok: inside the window, before the first timer's end
	s.click(srv.URL, t.TempDir())
	time.Sleep(200 * time.Millisecond) // sleep-ok: past the first click's timer, inside the second's
	ti, _ := f.get()
	assert.Contains(t, ti, "H2XR-HS16", "the earlier timer does not re-title a later click")
	assert.Eventually(t, func() bool { ti, _ := f.get(); return ti == setupCodeMenuTitle }, 2*time.Second, 10*time.Millisecond)
}
