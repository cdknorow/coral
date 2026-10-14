package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3418: a sign-in redirect only ever returns to a path on this origin.
func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"/":                "/",
		"/a/b?x=1#y":       "/a/b?x=1#y",
		"//evil.com/x":     "/",
		"/\\evil.com":      "/",
		"/a\\b":            "/",
		"http://evil.com/": "/",
		"evil.com":         "/",
		"":                 "/",
		"/a\nLocation: x":  "/",
		"/a\x7f":           "/",
		"/%2F%2Fevil.com":  "/%2F%2Fevil.com", // still a path here: the browser doesn't decode it into a host
	} {
		assert.Equal(t, want, SafeReturnTo(in), in)
	}
}

// The API-key flow's redirect uses it too.
func TestMiddlewareRedirectReturnToIsSafe(t *testing.T) {
	ks := &KeyStore{}
	h := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest("GET", "http://coral.example:8420/x", nil)
	r.RemoteAddr = "10.0.0.5:4444"
	r.RequestURI = "//evil.com/steal"
	r.URL.Path = "//evil.com/steal"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusTemporaryRedirect, rec.Code)
	assert.Equal(t, "/auth?return_to=%2F", rec.Header().Get("Location"))
}
