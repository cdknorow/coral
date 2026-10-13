package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The middleware records how it let each request in (#3182).
func TestMiddlewareRecordsTheMethod(t *testing.T) {
	ks, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	var got Method
	h := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = MethodFrom(r.Context()) }))
	serve := func(remote, target string, mod func(*http.Request)) Method {
		got = MethodNone
		r := httptest.NewRequest("GET", target, nil)
		r.RemoteAddr = remote
		r.Host = "127.0.0.1:8420"
		if mod != nil {
			mod(r)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		return got
	}
	assert.Equal(t, MethodLoopback, serve("127.0.0.1:5000", "/api/health", nil))
	assert.Equal(t, MethodAPIKey, serve("10.0.0.2:5000", "/api/health", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+ks.Key()) }))
	assert.Equal(t, MethodAPIKey, serve("10.0.0.2:5000", "/api/health?api_key="+ks.Key(), nil))
	tok, err := ks.CreateSession("10.0.0.2", "t")
	require.NoError(t, err)
	assert.Equal(t, MethodSession, serve("10.0.0.2:5000", "/api/health", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieName, Value: tok}) }))
	assert.Equal(t, MethodNone, serve("10.0.0.2:5000", "/api/health", nil), "refused: the handler never runs")
	assert.Equal(t, MethodNone, MethodFrom(httptest.NewRequest("GET", "/", nil).Context()))
}
