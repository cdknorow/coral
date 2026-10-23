package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
