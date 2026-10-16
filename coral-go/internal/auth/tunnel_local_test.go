package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3936 (Coral Hub Security's (b)): the tunnel reaches Coral from 127.0.0.1, so a request with
// its stamp, authentic or forged, is never "the person at this computer": it can't read or
// rotate the API key, isn't shown the locked page as local, and AuthStatus never reports
// locality as a credential in strict mode.
func TestTunnelStampIsNeverLocal(t *testing.T) {
	ks, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	ar := NewRoutes(ks)
	req := func(method, path string, stamp bool) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = "127.0.0.1:50000"
		r.Host = "localhost:8420"
		if stamp {
			r.Header.Set(tunnel.ViaHeader, "anything")
		}
		return r
	}

	assert.True(t, IsDirectLocal(req("GET", "/", false)))
	assert.False(t, IsDirectLocal(req("GET", "/", true)), "a tunnel stamp is not local")
	assert.True(t, IsDirectLocalPeer(req("GET", "/", true)), "the middleware's pass keeps the tunnel (its stamp is checked after)")

	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		path string
	}{
		{"read the key", ar.GetAPIKey, "/api/system/api-key"},
		{"rotate the key", ar.RegenerateKey, "/api/system/api-key/regenerate"},
	} {
		w := httptest.NewRecorder()
		tc.h(w, req("GET", tc.path, true))
		assert.Equal(t, http.StatusForbidden, w.Code, tc.name)
		assert.NotContains(t, w.Body.String(), ks.Key(), tc.name)
		w = httptest.NewRecorder()
		tc.h(w, req("GET", tc.path, false))
		assert.Equal(t, http.StatusOK, w.Code, tc.name+" from this computer")
	}

	ar.SetStrictLocal(true)
	w := httptest.NewRecorder()
	ar.AuthPage(w, req("GET", "/auth", true))
	assert.False(t, strings.Contains(w.Body.String(), LockedPageHTML) && LockedPageHTML != "", "the locked page is for this computer only")

	status := func(r *http.Request) map[string]any {
		w := httptest.NewRecorder()
		ar.AuthStatus(w, r)
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	st := status(req("GET", "/api/system/auth-status", false))
	assert.Equal(t, false, st["authenticated"], "strict: locality is not a credential")
	assert.Equal(t, "none", st["method"])
	r := req("GET", "/api/system/auth-status", false)
	r.Header.Set("X-API-Key", ks.Key())
	assert.Equal(t, "key", status(r)["method"])
	ar.SetStrictLocal(false)
	assert.Equal(t, "localhost", status(req("GET", "/api/system/auth-status", false))["method"], "opt-out: as before")
	assert.Equal(t, "none", status(req("GET", "/api/system/auth-status", true))["method"], "never for the tunnel")
}
