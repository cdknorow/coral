package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4032 (finding 5): the cookie is cleared with the flags it was set with.
func TestClearSessionCookieIsStrict(t *testing.T) {
	ks := newTestKeyStore(t)
	w := httptest.NewRecorder()
	ks.clearSessionCookie(w, httptest.NewRequest("GET", "/", nil))
	c := w.Result().Cookies()
	require.Len(t, c, 1)
	assert.Equal(t, http.SameSiteStrictMode, c[0].SameSite)
	assert.Equal(t, -1, c[0].MaxAge)
}
