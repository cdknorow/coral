package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3649: this Coral marks the refusals that are about how a caller reaches it, so a hub can say
// "key rejected" or "host refused" and nothing else: a wrong API key's 401 says key; a Host the
// DNS-rebinding check refuses (a port forward reached under another name) says host; a missing
// credential's 401 says neither.
func TestRefusalsAreMarkedForTheHub(t *testing.T) {
	ks, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	h := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	do := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	r := httptest.NewRequest("GET", "/api/sessions/live", nil)
	r.RemoteAddr = "203.0.113.7:5000"
	r.Header.Set("Authorization", "Bearer not-the-key")
	rec := do(r)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "key", rec.Header().Get(RefusedHeader))

	r = httptest.NewRequest("GET", "/api/sessions/live", nil)
	r.RemoteAddr = "203.0.113.7:5000"
	rec = do(r)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, rec.Header().Get(RefusedHeader), "no credential at all: not a key refusal")

	r = httptest.NewRequest("GET", "http://my-remote.example:18421/api/sessions/live", nil)
	r.RemoteAddr = "127.0.0.1:5000" // what a port forward looks like to this Coral
	r.Host = "my-remote.example:18421"
	rec = do(r)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "host", rec.Header().Get(RefusedHeader))

	r = httptest.NewRequest("GET", "/api/sessions/live", nil)
	r.RemoteAddr = "203.0.113.7:5000"
	r.Header.Set("Authorization", "Bearer "+ks.Key())
	rec = do(r)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get(RefusedHeader))
}
